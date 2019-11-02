package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ServerPool holds information about reachable backends
type ServerPool struct {
	servers []*url.URL
	mux     sync.Mutex
}

// Get a value from ServerPool
func (s *ServerPool) Get(i int) (u *url.URL) {
	s.mux.Lock()
	u = s.servers[i]
	s.mux.Unlock()
	return
}

// Add a backend to ServerPool
func (s *ServerPool) Add(serverUrl *url.URL) {
	s.mux.Lock()
	s.servers = append(s.servers, serverUrl)
	s.mux.Unlock()
}

// RemoveIndex from a ServerPool
func (s *ServerPool) RemoveIndex(i int) {
	s.mux.Lock()
	if i >= 0 && i < len(s.servers) {
		s.servers[i] = s.servers[len(s.servers)-1]
		s.servers = s.servers[:len(s.servers)-1]
	}
	s.mux.Unlock()
}

// Len of Servers in ServerPool
func (s *ServerPool) Len() (l int) {
	s.mux.Lock()
	l = len(s.servers)
	s.mux.Unlock()
	return
}

// Counter counts the index
type Counter struct {
	value int
	mux   sync.Mutex
}

var counter Counter
var healthyServers ServerPool
var unHealthyServers ServerPool
var healthCheckChan = make(chan *url.URL)

// getNext index
func getNext() int {
	counter.mux.Lock()
	if healthyServers.Len() != 0 {
		counter.value = (counter.value + 1) % healthyServers.Len()
	} else {
		counter.value = 0
	}
	counter.mux.Unlock()
	return counter.value
}

// healthCheck backends
func healthCheck()  {
	heartbeat := time.NewTicker(15 * time.Second)
	log.Printf("Health Checking Started\n")
	for {
		select {
		case u := <-healthCheckChan:
			log.Printf("Health Check: %s (down)\n", u.String())
			unHealthyServers.Add(u)
		case <-heartbeat.C:
			log.Println("Health check routine started")
			if unHealthyServers.Len() > 0 {
				var toBeMoved []int
				for i, value := range unHealthyServers.servers {
					// lets assume if / is reachable the server works, o/w we will have to use something like /status
					_, err := http.Get(value.String())
					if err == nil {
						log.Printf("Health Check: %s (up)\n", value.String())
						// remove the healthy server from the unhealthy pool and add to healthy pool
						toBeMoved = append(toBeMoved, i)
						healthyServers.Add(value)
					} else {
						log.Printf("Health Check: %s (down)\n", value.String())
					}
				}

				// clean the unhealthy pool
				for _, i := range toBeMoved {
					unHealthyServers.RemoveIndex(i)
				}
			}
		}

	}
}

// lb the requests with retries
func lb(w http.ResponseWriter, r *http.Request) {
	handleRequest(getNext(),5, w, r)
}

// copyHeader copies header from a request
func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// handle a request with retries
// will return 503 if any server is not reachable
func handleRequest(serverIndex int, retries int, w http.ResponseWriter, r *http.Request) {
	if retries < 0 || healthyServers.Len() == 0 {
		http.Error(w, "Service unavailable", http.StatusServiceUnavailable)
		return
	}

	u := healthyServers.Get(serverIndex)

	// hijack the request and change the host to a reachable backend
	r.URL.Scheme = u.Scheme
	r.URL.Host = u.Host
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		healthCheckChan <- u
		healthyServers.RemoveIndex(serverIndex)
		handleRequest(getNext(), retries - 1, w, r)
		return
	}

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("Backend: %s, Response: %v", u, err.Error())
	}
	_ = resp.Body.Close()
}

func main() {
	var serverList string
	var port int
	flag.StringVar(&serverList, "backends", "", "Load balanced backends, use semicolons to separate")
	flag.IntVar(&port, "port", 3030, "Port to serve")
	flag.Parse()

	if len(serverList) == 0 {
		log.Fatal("Please provide one or more backends to load balance")
	}

	// parse servers
	tokens := strings.Split(serverList, ";")
	for _, tok := range tokens {
		serverUrl, err := url.Parse(tok)
		if err != nil {
			log.Fatal(err)
		}
		// no need to think of race conditions in the initialize state
		healthyServers.servers = append(healthyServers.servers, serverUrl)
		log.Printf("Configured server: %s\n", serverUrl)
	}

	// start http server
	server := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(lb),
	}

	// start health checking
	go healthCheck()

	log.Printf("LB started at :%d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
