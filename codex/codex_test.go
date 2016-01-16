package codex

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/mdigger/rest"
)

func TestCodex(t *testing.T) {
	rest.Debug = true
	rest.SetLogger(os.Stderr)
	rest.Encoder = NewCoder(0)
	var mux rest.ServeMux
	mux.Handle("GET", "/test", func(c *rest.Context) error { return c.Send("test1") })
	mux.Handle("POST", "/test", func(c *rest.Context) error { return c.Send(errors.New("test2")) })
	mux.Handle("PUT", "/test", func(c *rest.Context) error { return c.Send(rest.JSON{"test": "message", "time": time.Now()}) })
	mux.Handle("PATCH", "/test", func(c *rest.Context) error { return c.Send(mux) })
	mux.Handle("TEST", "/test", func(c *rest.Context) error {
		return c.Send(struct {
			Test complex64
		}{
			Test: complex(3, 15),
		})
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "TEST"} {
		req, err := http.NewRequest(method, ts.URL+"/test", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept", "application/json")
		dump, err := httputil.DumpRequest(req, true)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s\n", dump)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
		}
		dump, err = httputil.DumpResponse(resp, true)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("%s\n%s\n", dump, strings.Repeat("-", 40))
		resp.Body.Close()
	}
}
