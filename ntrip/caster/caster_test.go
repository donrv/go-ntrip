package caster_test

import (
	"bytes"
	"crypto/tls"
	"errors"
	"github.com/umeat/go-ntrip/ntrip/caster"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

type MockAuth struct{}

func (ma MockAuth) Authorize(c *caster.Connection) error {
	if c.Request.URL.Path == "/401" {
		return errors.New("Unauthorized")
	}
	return nil
}

var (
	cast = caster.Caster{
		Mounts:     make(map[string]*caster.Mountpoint),
		Authorizer: MockAuth{},
		Timeout:    1 * time.Second,
	}
	data  = []byte("test data")
	conn  = caster.NewConnection(nil, httptest.NewRequest("POST", "/TEST", bytes.NewReader(data)))
	mount = &caster.Mountpoint{
		Source:      conn,
		Subscribers: make(map[string]caster.Subscriber),
	}
)

func TestRequestHandlerAuthorizedPOST(t *testing.T) {
	rr := httptest.NewRecorder()
	cast.RequestHandler(rr, httptest.NewRequest("POST", "/200", nil))
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusOK)
	}
}

func TestRequestHandlerAuthorizedGET(t *testing.T) {
	cast.AddMountpoint(mount)
	rr := httptest.NewRecorder()
	cast.RequestHandler(rr, httptest.NewRequest("GET", mount.Source.Request.URL.Path, nil))
	if rr.Code != http.StatusOK {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusOK)
	}
	cast.DeleteMountpoint(mount.Source.Request.URL.Path)
}

func TestRequestHandlerUnauthorized(t *testing.T) {
	rr := httptest.NewRecorder()
	cast.RequestHandler(rr, httptest.NewRequest("POST", "/401", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusUnauthorized)
	}

	rr = httptest.NewRecorder()
	cast.RequestHandler(rr, httptest.NewRequest("GET", "/401", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusUnauthorized)
	}
}

func TestRequestHandlerStatusConflict(t *testing.T) {
	cast.AddMountpoint(mount)
	rr := httptest.NewRecorder()
	cast.RequestHandler(rr, httptest.NewRequest("POST", mount.Source.Request.URL.Path, nil))
	if rr.Code != http.StatusConflict {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusConflict)
	}
	cast.DeleteMountpoint(mount.Source.Request.URL.Path)
}

func TestRequestHandlerStatusNotFound(t *testing.T) {
	rr := httptest.NewRecorder()
	cast.RequestHandler(rr, httptest.NewRequest("GET", "/404", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusNotFound)
	}
}

func TestRequestHandlerStatusNotImplemented(t *testing.T) {
	rr := httptest.NewRecorder()
	cast.RequestHandler(rr, httptest.NewRequest("HEAD", "/501", nil))
	if rr.Code != http.StatusNotImplemented {
		t.Errorf("handler returned wrong status code: got %v want %v",
			rr.Code, http.StatusNotImplemented)
	}
}

func TestCasterMountpointMethods(t *testing.T) {
	cast.AddMountpoint(mount)
	if m, exists := cast.Mounts[mount.Source.Request.URL.Path]; m != mount || !exists {
		t.Errorf("failed to add mountpoint")
	}
	if m := cast.GetMountpoint(mount.Source.Request.URL.Path); m != mount {
		t.Errorf("failed to get mountpoint")
	}
	cast.DeleteMountpoint(mount.Source.Request.URL.Path)
	if _, exists := cast.Mounts[mount.Source.Request.URL.Path]; exists {
		t.Errorf("failed to delete mountpoint")
	}
}

func TestMountpointMethods(t *testing.T) {
	err := mount.ReadSourceData()
	if err.Error() != "EOF" {
		t.Errorf("unexpected error while reading source data - " + err.Error())
	}

	client := caster.NewConnection(nil, nil)
	mount.RegisterSubscriber(client)

	err = mount.Broadcast(1 * time.Second)
	if err.Error() != "Timeout reading from source" {
		t.Errorf("unexpected error in Broadcast - " + err.Error())
	}

	select {
	case d := <-client.Channel():
		if !reflect.DeepEqual(d, data) {
			t.Errorf("read incorrect data from client channel: " + string(d))
		}
	default:
		t.Errorf("failed to read data from client channel")
	}

	mount.DeregisterSubscriber(client)
	if _, exists := mount.Subscribers[client.ID()]; exists {
		t.Errorf("failed to deregister subscriber")
	}
}

func TestHTTPServer(t *testing.T) {
	go cast.ListenHTTP(":2101")
	time.Sleep(100 * time.Millisecond)
	r, w := io.Pipe()
	done := make(chan bool, 1)
	go func() {
		for i := 0; i < 10; i += 1 {
			w.Write([]byte(time.Now().String() + "\r\n"))
			time.Sleep(100 * time.Millisecond)
		}
		r.Close()
		done <- true
	}()

	resp, err := http.Post("http://localhost:2101/http", "application/octet-stream", r)
	if err != nil {
		t.Errorf("failed to connect to caster - " + err.Error())
		return
	}
	if resp.StatusCode != 200 {
		t.Errorf("POST request returned wrong status code: got %v want %v",
			resp.StatusCode, http.StatusOK)
	}

	// client request to be disconnected by client disconnect
	resp, err = http.Get("http://localhost:2101/http")
	if err != nil {
		t.Errorf("failed to connect to mountpoint - " + err.Error())
		return
	}
	if resp.StatusCode != 200 {
		t.Errorf("GET request returned wrong status code: got %v want %v",
			resp.StatusCode, http.StatusOK)
	}

	resp.Body.Read([]byte{})
	resp.Body.Close()

	// client request to be disconnected by mount disconnect
	resp, err = http.Get("http://localhost:2101/http")
	if err != nil {
		t.Errorf("failed to connect to mountpoint - " + err.Error())
		return
	}
	<-done
}

func TestHTTPSServer(t *testing.T) {
	http.DefaultTransport.(*http.Transport).TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	go cast.ListenHTTPS(":2102", "../../cert.pem", "../../key.pem")
	time.Sleep(100 * time.Millisecond)
	_, err := http.Get("https://localhost:2102/https")
	if err != nil {
		t.Errorf("failed to connect to caster - " + err.Error())
		return
	}
}
