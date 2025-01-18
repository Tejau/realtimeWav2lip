// main.go
package main

import (
	"context"
	"encoding/binary"
	"log"
	"net/http"
	"sync"

	"github.com/deepgram/deepgram-go-sdk/client"
	"github.com/deepgram/deepgram-go-sdk/interfaces"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for demo
	},
}

type TranscriptionHub struct {
	clients    map[*websocket.Conn]bool
	broadcast  chan string
	register   chan *websocket.Conn
	unregister chan *websocket.Conn
	mutex      sync.Mutex
}

func newHub() *TranscriptionHub {
	return &TranscriptionHub{
		clients:    make(map[*websocket.Conn]bool),
		broadcast:  make(chan string),
		register:   make(chan *websocket.Conn),
		unregister: make(chan *websocket.Conn),
	}
}

func (h *TranscriptionHub) run() {
	for {
		select {
		case client := <-h.register:
			h.mutex.Lock()
			h.clients[client] = true
			h.mutex.Unlock()
		case client := <-h.unregister:
			h.mutex.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				client.Close()
			}
			h.mutex.Unlock()
		case message := <-h.broadcast:
			h.mutex.Lock()
			for client := range h.clients {
				err := client.WriteMessage(websocket.TextMessage, []byte(message))
				if err != nil {
					log.Printf("Error broadcasting to client: %v", err)
					client.Close()
					delete(h.clients, client)
				}
			}
			h.mutex.Unlock()
		}
	}
}

func handleWebSocket(hub *TranscriptionHub, w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading to WebSocket: %v", err)
		return
	}

	hub.register <- conn

	// Setup Deepgram client
	ctx := context.Background()
	transcriptOptions := &interfaces.LiveTranscriptionOptions{
		Language:    "en-US",
		Punctuate:   true,
		Encoding:    "linear16",
		Channels:    1,
		Sample_rate: 16000,
	}

	callback := func(result string) {
		hub.broadcast <- result
	}

	dgClient, err := client.NewWebSocketWithDefaults(ctx, transcriptOptions, callback)
	if err != nil {
		log.Printf("Error creating Deepgram client: %v", err)
		return
	}

	wsconn := dgClient.Connect()
	if wsconn == nil {
		log.Println("Deepgram connection failed")
		return
	}

	defer func() {
		hub.unregister <- conn
		conn.Close()
	}()

	// Handle incoming audio data
	for {
		messageType, p, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Error reading WebSocket message: %v", err)
			return
		}

		if messageType == websocket.BinaryMessage {
			// Convert audio data to int16 samples
			samples := make([]int16, len(p)/2)
			for i := 0; i < len(p); i += 2 {
				samples[i/2] = int16(binary.LittleEndian.Uint16(p[i:]))
			}

			// Send to Deepgram
			err = wsconn.Write(samples)
			if err != nil {
				log.Printf("Error sending to Deepgram: %v", err)
				return
			}
		}
	}
}

func main() {
	hub := newHub()
	go hub.run()

	// Serve static files
	http.Handle("/", http.FileServer(http.Dir("static")))

	// WebSocket endpoint
	http.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		handleWebSocket(hub, w, r)
	})

	log.Println("Server starting on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal("ListenAndServe:", err)
	}
}
