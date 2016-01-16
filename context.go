package rest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/gzip"
)

var (
	// Взведенный флаг Debug указывает, что описания ошибок возвращаются как
	// есть. В противном случае всегда возвращается только стандартное описание
	// статуса HTTP, сформированное на базе этой ошибки.
	Debug bool = false
	// Флаг Compress разрешает сжатие данных. Чтобы запретить сжимать данные,
	// установите значение данного флага в false. При инициализации сжатия
	// проверяется, что оно уже не включено, например, на уровне глобального
	// обработчика запросов и, в этом случае, сжатие не будет включено, даже
	// если флаг установлен.
	Compress bool = true
	// Encoder описывает функции, используемые для разбора запроса и кодирования
	// ответа. MaxBody задает максимальный размер поддерживаемого запроса.
	// Если размер превышает указанный, то возвращается ошибка. Если не хочется
	// ограничений, то можно установить значение 0, тогда проверка производиться
	// не будет.
	Encoder Coder = NewJSONCoder(1 << 15) // 32 мегабайта
	// EncodeError управляет форматом вывода ошибок: если флаг не взведен, то
	// ошибки отдаются как текст. В противном случае описание ошибок
	// кодируются с помощью Encoder и содержат статус и описание ошибки.
	EncodeError bool = true
)

// Context содержит контекстную информацию HTTP-запроса и методы формирования
// ответа на них. Т.к. http.Request импортируется в Context напрямую, то можно
// использовать все его свойства и методы, как родные свойства и методы самого
// контекста.
//
// Context скрывает http.ResponseWriter от прямого использования и, вместо
// этого, предоставляет свои методы для формирования ответа. Это позволяет
// обойти некоторые скользкие моменты и, иногда, несколько упростить код.
//
// При отдаче ответа анализируются первые байты данных и на основании них
// устанавливается тип ответа. Если вы хотите определить тип ответа
// самостоятельно, то проще всего установить значение ContentType строкой с
// описанием нужного типа.
//
// Есть два пути отослать в ответ ошибку: послать ее через Context.Send или
// вернуть ее из обработчика. Результат, в конечном счете, будет приблизительно
// одинаковый: разница только в том, что при возврате ошибки из обработчика,
// она будет записана в лог, а в случае посылки ее через Send — нет. Чтобы не
// путаться и не выбирать наилучший способ, просто рассматривайте возврат
// ошибки из обработчика, как мягкий вариант паники (panic).
type Context struct {
	*http.Request        // HTTP запрос в разобранном виде
	ContentType   string // тип информации в ответе

	response http.ResponseWriter         // ответ на запрос
	params   []param                     // именованные параметры из пути запроса
	path     string                      // путь запроса
	status   int                         // код HTTP-ответа
	sended   bool                        // флаг отосланного ответа
	query    url.Values                  // параметры запроса в URL (кеш)
	data     map[interface{}]interface{} // дополнительные данные пользователя
	size     int                         // размер переданных данных
	started  time.Time                   // время начала обработки запроса
	writer   io.Writer                   // интерфейс для записи ответов
	compress bool                        // флаг, что мы включили сжатие
}

// newContext возвращает новый инициализированный контекст. В отличии от просто
// создания нового контекста, вызов данного метода использует пул контекстов.
func newContext(w http.ResponseWriter, r *http.Request) *Context {
	c := contexts.Get().(*Context)
	// очищаем его от возможных старых данных
	c.Request = r
	c.ContentType = ""
	c.response = w
	c.path = r.URL.Path
	c.params = nil
	c.status = 0
	c.sended = false
	c.query = nil
	c.data = nil
	c.size = 0
	c.started = time.Now()
	// если сжатие еще не установлено, но поддерживается клиентом, то включаем его
	if Compress && w.Header().Get("Content-Encoding") == "" &&
		strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Add("Vary", "Accept-Encoding")
		gzw := gzips.Get().(*gzip.Writer)
		gzw.Reset(w)
		c.writer = gzw
		c.compress = true
	} else {
		c.writer = w
		c.compress = false
	}
	return c
}

// close возвращает контекст в пул используемых контекстов для дальнейшего
// использования. Вызывается автоматически после того, как контекст перестает
// использоваться.
func (c *Context) close() {
	// если ответ не был послан, то шлем ошибку
	if !c.sended {
		c.Status(http.StatusInternalServerError).Send(
			http.StatusText(http.StatusInternalServerError))
	}
	// если инициализировано сжатие, то закрываем и освобождаем компрессор
	if c.compress {
		if gzw, ok := c.writer.(*gzip.Writer); ok {
			gzw.Close()
			gzips.Put(gzw)
		}
	}
	c.log()         // выводим лог, если поддерживается
	contexts.Put(c) // помещаем контекст обратно в пул
}

// Header возвращает HTTP-заголовки ответа. Используется для поддержки
// интерфейса http.ResponseWriter.
func (c *Context) Header() http.Header {
	return c.response.Header()
}

// WriteHeader записывает заголовок ответа. Вызов метода автоматически взводит
// внутренний флаг, что отправка ответа начата. После его вызова отсылка
// каких-либо данных другим способом, кроме Write, уже не поддерживается.
// Используется для поддержки интерфейса http.ResponseWriter.
func (c *Context) WriteHeader(code int) {
	if c.sended {
		return
	}
	c.status = code
	if c.status == 0 {
		c.status = http.StatusOK
	} else if c.status < 100 || c.status >= 600 {
		c.status = http.StatusInternalServerError
	}
	c.sended = true
	c.response.WriteHeader(c.status)
}

// Write записывает данные в качестве ответа сервера. Может вызываться несколько
// раз. Используется для поддержки интерфейса http.ResponseWriter.
//
// При первом вызове (может быть не явный) автоматически устанавливается статус
// ответа. Если статус ответа был не задан, то будет использован статус 200
// (ОК). Так же, если не был задан ContentType, то он будет определен
// автоматически на основании анализа первых байт данных.
func (c *Context) Write(data []byte) (int, error) {
	if !c.sended {
		// выполняем только при первой отдаче данных
		header := c.response.Header()
		if header.Get("Content-Type") == "" {
			if c.ContentType == "" {
				// если тип не установлен, то анализируем его на основании
				// содержимого ответа
				c.ContentType = http.DetectContentType(data)
			}
			header.Set("Content-Type", c.ContentType)
		}
		// перед первой отдачей данных отдаем статус ответа
		c.WriteHeader(c.status)
	}
	// записываем данные в качестве ответа
	n, err := c.writer.Write(data)
	c.size += n
	return n, err
}

// Flush отдает накопленный буфер с ответом. Используется для поддержки
// интерфейса http.Flusher.
func (c *Context) Flush() {
	if flusher, ok := c.writer.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Status устанавливает код HTTP-ответа, который будет отправлен сервером. Вызов
// данного метода не приводит к немедленной отправке ответа, а только
// устанавливает внутренний статус. Статус должен быть в диапазоне от 200 до
// 599, в противном случае статус не изменяется.
//
// Метод возвращает ссылку на основной контекст, чтобы можно было использовать
// его в последовательности выполнения команд. Например, можно сразу установить
// код ответа и тут же опубликовать данные.
func (c *Context) Status(code int) *Context {
	if !c.sended && code >= 200 && code < 600 {
		c.status = code
	}
	return c
}

// Param возвращает значение именованного параметра. Если параметр с таким
// именем не найден, то возвращается значение параметра из URL с тем же именем.
//
// Разобранные параметры запроса пути сохраняются внутри Context и повторного
// его разбора уже не требует. Но это происходит только при первом к ним
// обращении.
func (c *Context) Param(key string) string {
	for _, param := range c.params {
		if param.Key == key {
			return param.Value
		}
	}
	if c.query == nil {
		c.query = c.Request.URL.Query()
	}
	return c.query.Get(key)
}

// Data возвращает пользовательские данные, сохраненные в контексте запроса с
// указанным ключем. Обычно эти данные используются, когда необходимо передать
// их между несколькими обработчиками.
func (c *Context) Data(key interface{}) interface{} {
	if c.data == nil {
		return nil
	}
	return c.data[key]
}

// SetData сохраняет пользовательские данные в контексте запроса с указанным
// ключем.
//
// Рекомендуется в качестве ключа использовать какой-нибудь приватный тип и его
// значение, чтобы избежать случайного затирания данных другими обработчиками:
// это гарантированно обезопасит от случайного доступа к ним. Но строки тоже
// поддерживаются. :)
func (c *Context) SetData(key, value interface{}) {
	if c.data == nil {
		c.data = make(map[interface{}]interface{})
	}
	c.data[key] = value
}

// Bind разбирает данные запроса и заполняет ими указанный в параметре объект.
// Разбор осуществляется с помощью Encoder.
func (c *Context) Bind(obj interface{}) error {
	return Encoder.Bind(c, obj)
}

// Эти ошибки обрабатываются при передаче их в метод Context.Send и
// устанавливают соответствующий статус ответа.
//
// Кроме указанных здесь ошибок, так же проверяется, что ошибка отвечает на
// os.IsNotExist (в этом случае статус станет 404) или os.IsPermission (статус
// 403). Все остальные ошибки устанавливают статус 500.
var (
	ErrDataAlreadySent       = errors.New("data already sent")
	ErrBadRequest            = errors.New("bad request")              // 400
	ErrUnauthorized          = errors.New("unauthorized")             // 401
	ErrForbidden             = errors.New("forbidden")                // 403
	ErrNotFound              = errors.New("not found")                // 404
	ErrLengthRequired        = errors.New("length required")          // 411
	ErrRequestEntityTooLarge = errors.New("request entity too large") // 413
	ErrUnsupportedMediaType  = errors.New("unsupported media type")   // 415
	ErrInternalServerError   = errors.New("internal server error")    // 500
	ErrNotImplemented        = errors.New("not implemented")          // 501
	ErrServiceUnavailable    = errors.New("service unavailable")      // 503
)

// Send отсылает переданные данные как ответ на запрос. В зависимости от типа
// данных, используются разные форматы ответов. Поддерживаются данные в формате
// string, error, []byte, io.Reader и nil. Все остальные типы данных приводятся
// к формату JSON.
//
// Данный метод можно использовать только один раз: после того, как ответ
// отправлен, повторный вызов данного метода сразу возвращает ошибку.
func (c *Context) Send(data interface{}) (err error) {
	// не можем отправить ответ, если он уже отправлен
	if c.sended {
		return ErrDataAlreadySent
	}
	// в зависимости от типа данных, отдаем их разными способами
	switch data := data.(type) {
	case nil:
		if c.status == 0 {
			c.status = http.StatusNoContent
		}
		c.WriteHeader(c.status)
	case string:
		if c.ContentType == "" {
			c.ContentType = "text/plain; charset=utf-8"
		}
		_, err = fmt.Fprint(c, data)
	case error:
		err = data // сводим задачу к обработке окончательной ошибки
		break
	case []byte:
		_, err = c.Write(data)
	case io.Reader:
		_, err = io.Copy(c, data)
	default: // кодируем как объект
		err = Encoder.Encode(c, data)
	}
	// если в процессе отправки произошла ошибка, но мы еще ничего не отправили,
	// то отдаем ошибку
	if err != nil && !c.sended {
		// устанавливаем статус, в зависимости от ошибки
		switch err {
		case ErrBadRequest: // 400
			c.status = http.StatusBadRequest
		case ErrUnauthorized: // 401
			c.status = http.StatusUnauthorized
		case ErrForbidden: // 403
			c.status = http.StatusForbidden
		case ErrNotFound: // 404
			c.status = http.StatusNotFound
		case ErrLengthRequired: // 411
			c.status = http.StatusLengthRequired
		case ErrRequestEntityTooLarge: // 413
			c.status = http.StatusRequestEntityTooLarge
		case ErrUnsupportedMediaType: // 415
			c.status = http.StatusUnsupportedMediaType
		case ErrInternalServerError: // 500
			c.status = http.StatusInternalServerError
		case ErrNotImplemented: // 501
			c.status = http.StatusNotImplemented
		case ErrServiceUnavailable: // 503
			c.status = http.StatusServiceUnavailable
		default:
			if os.IsNotExist(err) {
				c.status = http.StatusNotFound
			} else if os.IsPermission(err) {
				c.status = http.StatusForbidden
			} else {
				c.status = http.StatusInternalServerError
			}
		}
		// В зависимости от флага Debug, отдаем либо текст ошибки, либо статуса
		var msg string
		if Debug {
			msg = err.Error()
		} else {
			msg = http.StatusText(c.status)
		}
		// В зависимости от флага, ошибку выводим как JSON или как текст
		if EncodeError {
			err = Encoder.Encode(c, JSON{"code": c.status, "error": msg})
		} else {
			c.ContentType = "text/plain; charset=utf-8"
			_, err = fmt.Fprint(c, msg)
		}
	}
	return
}

// Error отправляет указанный текст как описание ошибки. В зависимости от
// флага EncodeError, данный текст будет отдан как описание или как JSON с кодом
// статуса. В отличии от обычных ошибок, на данный текст не распространяется
// правило отладки и текст будет отдан в неизменном виде, в не зависимости от
// установленного значения Debug.
func (c *Context) Error(code int, msg string) error {
	c.Status(code) // устанавливаем код ответа
	if EncodeError {
		return c.Send(JSON{"code": c.status, "error": msg})
	}
	c.ContentType = "text/plain; charset=utf-8"
	return c.Send(msg)
}

// Redirect отсылает ответ с требованием временного перехода по указанному URL.
// Ошибка никогда не возвращается.
func (c *Context) Redirect(url string) error {
	http.Redirect(c, c.Request, url, http.StatusFound)
	return nil
}

// пулы
var (
	contexts = sync.Pool{New: func() interface{} { return new(Context) }}
	gzips    = sync.Pool{New: func() interface{} { return new(gzip.Writer) }}
	buffers  = sync.Pool{New: func() interface{} { return new(bytes.Buffer) }}
)
