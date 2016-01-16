package rest

import (
	"encoding/json"
	"mime"
	"strings"
)

// JSON позволяет быстро описать данные в одноименном формате.
type JSON map[string]interface{}

// Coder описывает интерфейс для поддержки разбора запроса и кодирования ответа.
type Coder interface {
	Bind(*Context, interface{}) error
	Encode(*Context, interface{}) error
}

// JSONCoder осуществляет разбор запроса и кодирование ответа в формате JSON.
type JSONCoder struct {
	MaxBody int64 // максимально допустимый размер запроса
}

// NewJSONCoder возвращает новый инициализированный Coder, поддерживающий
// формат JSON.
func NewJSONCoder(maxSize int64) *JSONCoder {
	return &JSONCoder{MaxBody: maxSize}
}

// Bind разбирает данные запроса в формате JSON и заполняет ими указанный в
// параметре объект.
//
// Если Content-Type запроса не соответствует "application/json", то
// возвращается ошибка ErrUnsupportedMediaType. Так же может возвращать ошибку
// ErrLengthRequired, если не указана длина запроса, ErrRequestEntityTooLarge —
// если запрос превышает значение MaxBody, и ErrBadRequest — если не смогли
// разобрать запрос и поместить результат разбора в объект obj. Все эти ошибки
// поддерживаются методом Send и отдают соответствующий статус ответа на запрос.
func (j JSONCoder) Bind(c *Context, obj interface{}) error {
	r := c.Request // запрос
	// разбираем заголовок с типом информации в запросе
	mediatype, params, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	charset, ok := params["charset"]
	if !ok {
		charset = "UTF-8"
	}
	// если запрос не является JSON, то возвращаем ошибку
	if mediatype != "application/json" || strings.ToUpper(charset) != "UTF-8" {
		return ErrUnsupportedMediaType
	}
	// если запрос превышает допустимый объем, то возвращаем ошибку
	if j.MaxBody > 0 {
		if r.ContentLength == 0 {
			return ErrLengthRequired
		} else if r.ContentLength > j.MaxBody {
			return ErrRequestEntityTooLarge
		}
	}
	// разбираем данные из запроса
	if err := json.NewDecoder(r.Body).Decode(obj); err != nil {
		return ErrBadRequest
	}
	return nil
}

// Encode кодирует и отправляет ответ с содержимым obj в формате JSON.
func (JSONCoder) Encode(c *Context, obj interface{}) error {
	if c.ContentType == "" {
		c.ContentType = "application/json; charset=utf-8"
	}
	return json.NewEncoder(c).Encode(obj)
}
