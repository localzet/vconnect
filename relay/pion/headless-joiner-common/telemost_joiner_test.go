package joiner

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func TestCloseCancelsInFlightAPIRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	joiner := NewTelemostHeadlessJoiner(nil, nil, nil, nil, nil, nil)
	joiner.httpClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		close(requestStarted)
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}

	requestDone := make(chan error, 1)
	go func() {
		_, _, err := joiner.tmRequest(http.MethodGet, "/test")
		requestDone <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("API request did not start")
	}

	joiner.Close()
	select {
	case err := <-requestDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("request error = %v, want context cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the API request")
	}
}

func TestCloseInterruptsWaitingRoomDelay(t *testing.T) {
	joiner := NewTelemostHeadlessJoiner(nil, nil, nil, nil, nil, nil)
	waitDone := make(chan bool, 1)
	go func() {
		waitDone <- joiner.waitForStopOrDelay(time.Hour)
	}()

	joiner.Close()
	select {
	case continued := <-waitDone:
		if continued {
			t.Fatal("waiting room delay continued after Close")
		}
	case <-time.After(time.Second):
		t.Fatal("Close did not interrupt the waiting room delay")
	}
}

func TestCloseUnblocksTelemostWebSocketLoop(t *testing.T) {
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	helloReceived := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		connection, err := upgrader.Upgrade(writer, request, nil)
		if err != nil {
			return
		}
		defer connection.Close()
		if _, _, err := connection.ReadMessage(); err != nil {
			return
		}
		close(helloReceived)
		for {
			if _, _, err := connection.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()

	joiner := NewTelemostHeadlessJoiner(func(string, ...any) {}, func(host string) (string, error) {
		return host, nil
	}, nil, nil, nil, nil)
	joiner.mediaURL = "wss" + strings.TrimPrefix(server.URL, "https")
	joiner.displayName = "test"
	joiner.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{}`)),
			Header:     make(http.Header),
		}, nil
	})}

	connectDone := make(chan struct{})
	go func() {
		joiner.connectAndRun()
		close(connectDone)
	}()

	select {
	case <-helloReceived:
	case <-time.After(2 * time.Second):
		joiner.Close()
		t.Fatal("WebSocket hello was not received")
	}

	joiner.Close()
	select {
	case <-connectDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not unblock the WebSocket loop")
	}
}
