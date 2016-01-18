package rest

import (
	"net/http"
	"strings"
)

// ServeMux описывает список обработчиков, ассоциированных с путями запроса и
// методами.
type ServeMux struct {
	// Позволяет задать базовый путь для всех запросов.
	BasePath string
	// Описывает дополнительные заголовки HTTP-ответа, которые будут добавлены
	// ко всем ответам, возвращаемым данным обработчиком
	Headers map[string]string

	routers map[string]*router // обработчики запросов по методам
}

// ServeHTTP обеспечивает поддержку интерфейса http.Handler. Таким образом,
// данный ServeMux можно использовать как стандартный обработчик HTTP-запросов.
//
// В процессе обработки запроса отслеживаются возвращаемые ошибки и
// перехватываются возможные вызовы panic. Если ответ на запрос еще не
// отправлялся, то в этих случаях в ответ будет отправлена ошибка.
func (m ServeMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c := newContext(w, r) // формируем контекст для ответа
	defer func() {
		// перехватываем panic, если она случилась
		if e := recover(); e != nil {
			// если еще ничего не отсылали, то отсылаем эту ошибку
			c.Send(e)
			// выводим дамп с ошибкой
			if accessLog != nil {
				c.errorLog(e, 3) // записываем ошибку в лог
			}
		}
		c.close() // освобождаем по окончании
	}()
	// вызываем обработку запроаса
	if err := m.Handler(c); err != nil {
		if accessLog != nil {
			c.errorLog(err, 1) // записываем ошибку в лог
		}
		// если ничего не отправляли, то отправляем эту ошибку
		if !c.sended {
			c.Send(err) // возвращаемые ошибки игнорируем
		}
	}
}

// Handler отвечает за подбор обработчика и его выполнение.
//
// Если обработчик для данного пути и метода не найден, но есть обработчики
// для других методов, то возвращается статус http.StatusMethodNotAllowed и в
// заголовке передается список методов, которые можно применить к данному пути.
// В противном случае возвращается статус http.StatusNotFound.
func (m ServeMux) Handler(c *Context) error {
	// добавляем заголовки, если они определены
	header := c.response.Header()
	if len(m.Headers) > 0 {
		for key, value := range m.Headers {
			header.Set(key, value)
		}
	}
	// если задан базовый путь, то удаляем его из пути обработки
	if m.BasePath != "" {
		// проверяем, что путь начинается с базового пути
		if !strings.HasPrefix(c.path, m.BasePath) {
			return c.Send(ErrNotFound)
		}
		c.path = strings.TrimPrefix(c.path, m.BasePath)
	}
	// получаем список обработчиков для данного метода
	routers := m.routers[c.Request.Method]
	// запрашиваем подходящий обработчик
	if handler, params := routers.lookup(c.path); handler != nil {
		// добавляем найденные параметры к контексту
		c.params = append(c.params, params...)
		// вызываем обработчик запроса
		return handler(c)
	}
	// обработчик для данного пути не найден
	// собираем список методов, которые поддерживаются для данного пути
	methods := make([]string, 0, len(m.routers))
	for method, handlers := range m.routers {
		if handler, _ := handlers.lookup(c.path); handler != nil {
			methods = append(methods, method)
		}
	}
	if len(methods) > 0 {
		// если есть обработчики для данного пути, но с другими методами,
		// то отдаем этот список методов
		header.Set("Allow", strings.Join(methods, ", "))
		return c.Status(http.StatusMethodNotAllowed).Send(nil)
	}
	// обработчики пути не определены ни для одного метода
	return c.Send(ErrNotFound)
}

// Handle регистрирует обработчик для указанного метода и пути.
// В описании пути можно использовать именованные параметры (начинаются с
// символа ':') и завершающий именованный параметр (начинается с '*'), который
// указывает, что path может быть длиннее. В последнем случае вся остальная
// часть пути будет включена в данный параметр. Параметр со звездочкой, если
// указан, должен быть самым последним параметром пути.
//
// Если количество элементов пути в path больше 32768 или параметр со звездочкой
// используется не в самом последнем элементе пути, то возникает panic.
func (m *ServeMux) Handle(method, path string, handler Handler) {
	if method == "" || handler == nil { // игнорируем пустые обработчики
		return
	}
	// если список обработчиков еще не инициализирован, то инициализируем его
	if m.routers == nil {
		// обычно используется не более 9 методов HTTP
		m.routers = make(map[string]*router, 9)
	}
	// получаем список обработчиков для данного метода
	r := m.routers[strings.ToUpper(method)]
	if r == nil {
		r = new(router)
		m.routers[strings.ToUpper(method)] = r
	}
	// добавляем обработчик для заданного метода и пути
	if err := r.add(path, handler); err != nil {
		panic(err) // обработчик нас не устраивает по каким-то причинам
	}
}

// Handles добавляет сразу список обработчиков для нескольких путей и методов.
// Это, по сути, просто удобный способ сразу определить большое количество
// обработчиков, не вызывая каждый раз ServeMux.Handle.
func (m *ServeMux) Handles(paths Paths) {
	for path, methods := range paths {
		for method, handler := range methods {
			m.Handle(method, path, handler)
		}
	}
}

type (
	// Paths позволяет описать сразу несколько обработчиков для разных путей
	// и методов: ключем для данного словаря как раз являются пути запросов.
	// Используется в качестве аргумента при вызове метода ServeMux.Handles.
	Paths map[string]Methods
	// Methods позволяет описать обработчики для методов.
	Methods map[string]Handler
)

type dataSet byte // для внутреннего использования с установкой данных
