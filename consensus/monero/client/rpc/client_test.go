package rpc_test

import (
	"context"
	"fmt"
	"git.gammaspectra.live/P2Pool/consensus/v4/utils"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"git.gammaspectra.live/P2Pool/consensus/v4/monero/client/rpc"
)

func assertError(t *testing.T, err error, msgAndArgs ...any) {
	if err == nil {
		message := ""
		if len(msgAndArgs) > 0 {
			message = fmt.Sprint(msgAndArgs...) + ": "
		}
		t.Errorf("%sexpected err", message)
	}
}

func assertContains(t *testing.T, actual, expected string, msgAndArgs ...any) {
	if !strings.Contains(actual, expected) {
		message := ""
		if len(msgAndArgs) > 0 {
			message = fmt.Sprint(msgAndArgs...) + ": "
		}
		t.Errorf("%sactual: %v expected: %v", message, actual, expected)
	}
}

func assertEqual(t *testing.T, actual, expected any, msgAndArgs ...any) {
	if !reflect.DeepEqual(actual, expected) {
		message := ""
		if len(msgAndArgs) > 0 {
			message = fmt.Sprint(msgAndArgs...) + ": "
		}
		t.Errorf("%sactual: %v expected: %v", message, actual, expected)
	}
}

func it(t *testing.T, msg string, f func(t *testing.T)) {
	t.Run(msg, func(t *testing.T) {
		f(t)
	})
}

// nolint:funlen
func TestClient(t *testing.T) {
	t.Parallel()

	t.Run("JSONRPC", func(t *testing.T) {
		var (
			ctx    = context.Background()
			client *rpc.Client
			err    error
		)

		it(t, "errors when daemon down", func(t *testing.T) {
			daemon := httptest.NewServer(http.HandlerFunc(nil))
			daemon.Close()

			client, err = rpc.NewClient(daemon.URL, rpc.WithHTTPClient(daemon.Client()))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			err = client.JSONRPC(ctx, "method", nil, nil)
			assertError(t, err)
			assertContains(t, err.Error(), "do:")
		})

		it(t, "errors w/ empty response", func(t *testing.T) {
			handler := func(w http.ResponseWriter, r *http.Request) {}

			daemon := httptest.NewServer(http.HandlerFunc(handler))
			defer daemon.Close()

			client, err = rpc.NewClient(daemon.URL, rpc.WithHTTPClient(daemon.Client()))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			err = client.JSONRPC(ctx, "method", nil, nil)
			assertError(t, err)
			assertContains(t, err.Error(), "decode")
		})

		it(t, "errors w/ non-200 response", func(t *testing.T) {
			handler := func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(500)
			}

			daemon := httptest.NewServer(http.HandlerFunc(handler))
			defer daemon.Close()

			client, err = rpc.NewClient(daemon.URL, rpc.WithHTTPClient(daemon.Client()))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			err = client.JSONRPC(ctx, "method", nil, nil)
			assertError(t, err)
			assertContains(t, err.Error(), "non-2xx status")
		})

		it(t, "makes GET request to the jsonrpc endpoint", func(t *testing.T) {
			var (
				endpoint string
				method   string
			)

			handler := func(w http.ResponseWriter, r *http.Request) {
				endpoint = r.URL.Path
				method = r.Method
			}

			daemon := httptest.NewServer(http.HandlerFunc(handler))
			defer daemon.Close()

			client, err = rpc.NewClient(daemon.URL, rpc.WithHTTPClient(daemon.Client()))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			err = client.JSONRPC(ctx, "method", nil, nil)
			assertEqual(t, rpc.EndpointJSONRPC, endpoint)
			assertEqual(t, method, "GET")
		})

		it(t, "encodes rpc in request", func(t *testing.T) {
			var (
				body = &rpc.RequestEnvelope{}

				params = map[string]interface{}{
					"foo": "bar",
					"caz": 123.123,
				}
			)

			handler := func(w http.ResponseWriter, r *http.Request) {
				err := utils.NewJSONDecoder(r.Body).Decode(body)
				if err != nil {
					t.Errorf("unexpected err: %v", err)
				}
			}

			daemon := httptest.NewServer(http.HandlerFunc(handler))
			defer daemon.Close()

			client, err = rpc.NewClient(daemon.URL, rpc.WithHTTPClient(daemon.Client()))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			err = client.JSONRPC(ctx, "rpc-method", params, nil)
			assertEqual(t, body.ID, "0")
			assertEqual(t, body.JSONRPC, "2.0")
			assertEqual(t, body.Method, "rpc-method")
			assertEqual(t, body.Params, params)
		})

		it(t, "captures result", func(t *testing.T) {
			handler := func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintln(w, `{"id":"id", "jsonrpc":"jsonrpc", "result": {"foo": "bar"}}`)
			}

			daemon := httptest.NewServer(http.HandlerFunc(handler))
			defer daemon.Close()

			client, err = rpc.NewClient(daemon.URL, rpc.WithHTTPClient(daemon.Client()))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			result := map[string]string{}

			err = client.JSONRPC(ctx, "rpc-method/home/shoghicp/radio/p2pool-observer", nil, &result)
			if err != nil {
				t.Errorf("unexpected err: %v", err)
			}

			assertEqual(t, result, map[string]string{"foo": "bar"})
		})

		it(t, "fails if rpc errored", func(t *testing.T) {
			handler := func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintln(w, `{"id":"id", "jsonrpc":"jsonrpc", "error": {"code": -1, "message":"foo"}}`)
			}

			daemon := httptest.NewServer(http.HandlerFunc(handler))
			defer daemon.Close()

			client, err = rpc.NewClient(daemon.URL, rpc.WithHTTPClient(daemon.Client()))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}

			result := map[string]string{}

			err = client.JSONRPC(ctx, "rpc-method", nil, &result)
			assertError(t, err)

			assertContains(t, err.Error(), "foo")
			assertContains(t, err.Error(), "-1")
		})
	})
}
