package rest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// JSON является вспомогательным типом для быстрого создания JSON-структур.
type JSON map[string]interface{}

// Compress разрешает поддержку сжатия данных, если это поддерживается браузером.
// Если сжатие данных уже поддерживается, например, на уровне вашего обработчика, то вы можете
// заблокировать двойное сжатие, установив значение false.
var Compress = true

// Context содержит контекстную информацию HTTP-запроса и методы удобного формирования ответа
// на них. Т.к. http.Request импортируется в контекст напрямую, то можно совершенно спокойно
// использовать все его свойства и методы, как родные свойства и методы самого контекста.
type Context struct {
	// HTTP запрос в разобранном виде
	*http.Request
	// именованные параметры из пути запроса
	Params []Param
	// тип информации в ответе
	ContentType string
	// интерфейс для публикации ответа на запрос
	response http.ResponseWriter
	// код HTTP-ответа
	status int
	// разобранные параметры запроса в URL (кеш)
	urlQuery url.Values
	// дополнительные данные, устанавливаемые пользователем
	// в качестве ключа рекомендуется использовать приватный тип
	// и какое-нибудь его значение, что позволит застраховаться от
	// случайной перезаписи этих данных
	data map[interface{}]interface{}
}

// newContext возвращает новый инициализированный контекст. В отличии от просто создания нового
// контекста, вызов данного метода использует пул контекстов.
func newContext(w http.ResponseWriter, r *http.Request) *Context {
	context := contexts.Get().(*Context)
	context.Request = r
	context.Params = nil
	context.ContentType = ""
	context.response = w
	context.status = 0
	context.urlQuery = nil
	context.data = nil
	return context
}

// free возвращает контекст в пул используемых контекстов для дальнейшего использования.
// Вызывается автоматически после того, как контекст перестает использоваться.
func (c *Context) free() {
	contexts.Put(c)
}

// Get возвращает значение именованного параметра. Если параметр с таким именем не найден,
// то возвращается значение параметра из URL с тем же именем. Разбор параметров запроса сохраняется
// внутри Context и повторного его разбора уже не требует. Но это происходит только при первом
// к ним обращении.
func (c *Context) Get(key string) string {
	for _, param := range c.Params {
		if param.Key == key {
			return param.Value
		}
	}
	if c.urlQuery == nil {
		c.urlQuery = c.Request.URL.Query()
	}
	return c.urlQuery.Get(key)
}

// Data возвращает пользовательские данные, сохраненные в контексте запроса с указанным ключем.
// Обычно, такие данные сохраняются в контексте запроса, если его нужно передать между несколькими
// обработчиками. В частности, очень удобно использовать c Middleware-обработчиками.
func (c *Context) Data(key interface{}) interface{} {
	if c.data == nil {
		return nil
	}
	return c.data[key]
}

// SetData сохраняет пользовательские данные в контексте запроса с указанным ключем.
// Рекомендуется в качестве ключа использовать какой-нибудь приватный тип и его значение,
// чтобы избежать случайного затирания данных другими обработчиками: это гарантированно обезопасит
// от случайного доступа к ним.
func (c *Context) SetDataSet(key, value interface{}) {
	if c.data == nil {
		c.data = make(map[interface{}]interface{})
	}
	c.data[key] = value
}

// Status устанавливает код HTTP-ответа, который будет отправлен сервером. Данный метод возвращает
// ссылку на основной контекст, чтобы можно было использовать его в последовательности выполнения
// команд. Например, можно сразу установить код ответа и тут же опубликовать данные.
func (c *Context) Status(code int) *Context {
	if code >= 200 && code < 600 {
		c.status = code
	}
	return c
}

// SetHeader устанавливает новое значение для указанного HTTP-заголовка. Если передаваемое
// значение заголовка пустое, то данный заголовок будет удален.
func (c *Context) SetHeader(key, value string) {
	if value == "" {
		c.response.Header().Del(key)
	} else {
		c.response.Header().Set(key, value)
	}
}

// Parse декодирует содержимое запроса в объект. После чтения из запроса
// http.Request.Body автоматически закрывается и дополнительного закрытия не требуется.
//
// На данный момент поддерживается только разбор объектов в формате JSON.
func (c *Context) Parse(obj interface{}) error {
	defer c.Request.Body.Close()
	return json.NewDecoder(c.Request.Body).Decode(obj)
}

// Send публикует данные, переданные в параметре, в качестве ответа. Если ContentType не указан,
// то используется "application/json".
//
// В зависимости от типа передаваемых данных, ответ формируется по разному.
// Если данные являются бинарными ([]byte) или поддерживают интерфейс io.Reader, то отдаются
// как есть, без какого-либо изменения. Если io.Reader поддерживает io.Close, то он будет
// автоматически закрыт. Строки и ошибки преобразуются в простое JSON-сообщение, состоящие из кода
// статуса и текста сообщения. Остальные типы приводятся к формату JSON.
//
// Вызов данного метода сразу инициализирует отдачу содержимого в качестве ответа. Поэтому не
// рекомендуется вызывать его несколько раз, т.к. попытка второй раз записать статус ответа
// приведет к ошибке.
//
// Если клиент поддерживает сжатие данных, то автоматически включается поддержка сжатия ответа.
// Чтобы отключить данное поведение, установите флаг Compress в false.
func (c *Context) Send(data interface{}) (err error) {
	defer func() {
		if recover := recover(); recover != nil {
			var ok bool
			if err, ok = recover.(error); !ok {
				err = fmt.Errorf("rest: %v", recover)
			}
		}
	}()
	var headers = c.response.Header() // быстрый доступ к заголовкам ответа
	if c.ContentType == "" {
		c.ContentType = "application/json; charset=utf-8"
	}
	headers.Set("Content-Type", c.ContentType)
	// поддерживаем компрессию, если она поддерживается клиентом и не запрещена в библиотеке
	var writer io.Writer = c.response
	if Compress {
		switch accept := c.Request.Header.Get("Accept-Encoding"); {
		case strings.Contains(accept, "gzip"): // Поддерживается gzip-сжатие
			headers.Set("Content-Encoding", "gzip")
			headers.Add("Vary", "Accept-Encoding")
			writer = gzipGet(writer)
			defer gzipPut(writer.(io.Closer))
		case strings.Contains(accept, "deflate"): // Поддерживается deflate-сжатие
			headers.Set("Content-Encoding", "deflate")
			headers.Add("Vary", "Accept-Encoding")
			writer = deflateGet(writer)
			defer deflatePut(writer.(io.Closer))
		}
	}
	// обрабатываем статус выполнения запроса
	if c.status == 0 {
		c.status = http.StatusOK
	}
	c.response.WriteHeader(c.status)
	enc := json.NewEncoder(writer) // инициализируем JSON-encoder
	// в зависимости от типа данных поддерживаются разные методы вывода
	switch data := data.(type) {
	case nil: // нечего отдавать
		if c.status >= 400 { // если статус соответствует ошибке, то формируем текст с ее описанием
			err = enc.Encode(JSON{"code": c.status, "error": http.StatusText(c.status)})
		}
	case io.Reader: // поток данных отдаем как есть
		_, err = io.Copy(writer, data)
		if data, ok := data.(io.Closer); ok {
			data.Close() // закрываем по окончании, раз поддерживается
		}
	case []byte: // уже готовый к отдаче набор данных
		_, err = writer.Write(data) // тоже отдаем как есть
	case error: // ошибки возвращаем в виде специального JSON
		err = enc.Encode(JSON{"code": c.status, "error": data.Error()})
	case string: // строки тоже возвращаем в виде специального JSON
		m := JSON{"code": c.status}
		if c.status >= 400 { // в случае ошибок это будет error
			m["error"] = data
		} else { // с случае просто текстовых сообщений — message
			m["message"] = data
		}
		err = enc.Encode(m)
	default: // во всех остальных случаях отдаем JSON-представление
		err = enc.Encode(data)
	}
	return err
}

// contexts содержит пул контекстов
var contexts = sync.Pool{New: func() interface{} { return new(Context) }}
