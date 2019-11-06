package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const Attempts = "ATTEMPTS"

type Backend struct {
	URL          *url.URL
	Alive        bool
	ReverseProxy *httputil.ReverseProxy
}

// ServerPool holds information about reachable backends
type ServerPool struct {
	backends []*Backend
	mux      sync.RWMutex
	current  int32
}

func (s *ServerPool) NextIndex() int {
	if len(s.backends) == 0 {
		return 0
	}
	// atomically increase the counter with bounds
	atomic.StoreInt32(&s.current, (atomic.LoadInt32(&s.current)+1)%int32(len(s.backends)))
	return int(atomic.LoadInt32(&s.current))
}

func (s *ServerPool) MarkBackendStatus(backendUrl *url.URL, alive bool) {
	s.mux.Lock()
	for i := 0; i < len(s.backends); i++ {
		if s.backends[i].URL.String() == backendUrl.String() && s.backends[i].Alive != alive {
			s.backends[i].Alive = alive
			break
		}
	}
	s.mux.Unlock()
}

func (s *ServerPool) GetNextPeer() *Backend {
	s.mux.RLock()
	backends := s.backends
	s.mux.RUnlock()

	// loop entire backends to find out an alive backend
	next := s.NextIndex()
	l := len(backends) + next
	for i := next; i < l; i++ {
		idx := i % len(backends)
		if s.backends[idx].Alive {
			atomic.StoreInt32(&s.current, int32(idx))
			return backends[idx]
		}
	}
	return nil
}

func GetAttemptsFromContext(r *http.Request) int {
	if attempts, ok := r.Context().Value(Attempts).(int); ok {
		return attempts
	}
	return 1
}

func lb(w http.ResponseWriter, r *http.Request) {
	attempts := GetAttemptsFromContext(r)
	if attempts > 5 {
		log.Printf("%s(%s) Max attempts reached, terminating\n", r.RemoteAddr, r.URL.Path)
		http.Error(w, "Service not available", http.StatusServiceUnavailable)
		return
	}

	peer := serverPool.GetNextPeer()
	if peer != nil {
		peer.ReverseProxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Service not available", http.StatusServiceUnavailable)
}

func isAlive(u *url.URL) bool {
	timeout := 2 * time.Second
	conn, err := net.DialTimeout("tcp", u.Host, timeout)
	if err != nil {
		log.Println("Site unreachable, error: ", err)
		return false
	}
	_ = conn.Close()
	return true
}

func healthCheck() {
	t := time.NewTicker(time.Minute * 2)
	for {
		select {
		case <-t.C:
			log.Println("Starting health check...")
			serverPool.HealthCheck()
			log.Println("Health check completed")
		}
	}
}

func (s *ServerPool) HealthCheck() {
	s.mux.RLock()
	backends := s.backends
	s.mux.RUnlock()
	for i := 0; i < len(backends); i++ {
		status := "up"
		alive := isAlive(backends[i].URL)
		backends[i].Alive = alive
		if !alive {
			status = "down"
		}
		log.Printf("%s [%s]\n", backends[i].URL, status)
	}
	s.mux.Lock()
	s.backends = backends
	s.mux.Unlock()
}

var serverPool ServerPool

func main() {
	var serverList string
	var port int
	flag.StringVar(&serverList, "backends", "", "Load balanced backends, use commas to separate")
	flag.IntVar(&port, "port", 3030, "Port to serve")
	flag.Parse()

	if len(serverList) == 0 {
		log.Fatal("Please provide one or more backends to load balance")
	}

	// parse servers
	tokens := strings.Split(serverList, ",")
	for _, tok := range tokens {
		serverUrl, err := url.Parse(tok)
		if err != nil {
			log.Fatal(err)
		}

		proxy := httputil.NewSingleHostReverseProxy(serverUrl)
		proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, e error) {
			log.Printf("[%s] %s\n", serverUrl.Host, e.Error())
			serverPool.MarkBackendStatus(serverUrl, false)
			attempts := GetAttemptsFromContext(request)
			log.Printf("%s(%s) Attempting retry %d\n", request.RemoteAddr, request.URL.Path, attempts)
			ctx := context.WithValue(request.Context(), Attempts, attempts+1)
			lb(writer, request.WithContext(ctx))
		}

		serverPool.backends = append(serverPool.backends, &Backend{
			URL:          serverUrl,
			Alive:        true,
			ReverseProxy: proxy,
		})
		log.Printf("Configured server: %s\n", serverUrl)
	}

	// start http server
	server := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(lb),
	}

	// start health checking
	go healthCheck()

	log.Printf("Load Balancer started at :%d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
