package httpservice

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
)

var upgrade = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		// Allow connections only from trusted origins
		//origin := r.Header.Get("Origin")
		//allowedOrigins := map[string]bool{
		//	"https://trusted-domain.com": true,
		//	"https://another-trusted.com": true,
		//}
		//return allowedOrigins[origin]
		return true
	},
}

var Peers sync.Map

type PeerConnection struct {
	Conn       *websocket.Conn
	LastActive time.Time
}

func SignalingHandler(c echo.Context) error {
	conn, err := upgrade.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		log.Println("Error upgrading connection:", err)
		return echo.NewHTTPError(http.StatusInternalServerError, "Failed to upgrade to WebSocket")
	}
	var peerId string
	clientPeerId := c.QueryParam("peerId")
	if clientPeerId == "" {
		uid, err := uuid.NewV7()
		if err != nil {
			log.Printf("Error generating UUID: %v", err)
			err := conn.Close()
			if err != nil {
				return err
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to generate peerId")
		}
		peerId = uid.String()

		err = conn.WriteMessage(websocket.TextMessage, []byte(peerId))
		if err != nil {
			log.Printf("Error sending UUID to client: %v", err)
			err := conn.Close()
			if err != nil {
				return err
			}
			return echo.NewHTTPError(http.StatusInternalServerError, "Failed to send peerId")
		}
	} else {
		if _, err := uuid.Parse(peerId); err != nil {
			log.Printf("Invalid peerId: %s", peerId)
			err := conn.Close()
			if err != nil {
				return err
			}
			return echo.NewHTTPError(http.StatusBadRequest, "Invalid peerId")
		}
		peerId = clientPeerId
	}

	// Close old connection if it exists
	if v, ok := Peers.Load(peerId); ok {
		oldPeer := v.(PeerConnection)
		err := oldPeer.Conn.Close()
		if err != nil {
			return err
		}
		log.Printf("Closed old connection for peerId: %s", peerId)
	}

	Peers.Store(peerId, PeerConnection{Conn: conn, LastActive: time.Now()})
	log.Printf("Stored new connection for peerId: %s", peerId)

	defer func() {
		if v, ok := Peers.Load(peerId); ok {
			peer := v.(PeerConnection)
			err := peer.Conn.Close()
			if err != nil {
				return
			}
			Peers.Delete(peerId)
			log.Printf("Removed connection for peerId: %s", peerId)
		}
	}()

	// Heartbeat to update LastActive
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for {
			<-ticker.C
			if _, ok := Peers.Load(peerId); !ok {
				return // Stop heartbeat if peer is removed
			}
			Peers.Store(peerId, PeerConnection{Conn: conn, LastActive: time.Now()})
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("Error reading message from %s: %v", peerId, err)
			break
		}

		// Parse incoming message
		var payload map[string]interface{}
		if err := json.Unmarshal(msg, &payload); err != nil {
			log.Printf("Invalid message format from %s: %v", peerId, err)
			err := conn.WriteMessage(websocket.TextMessage, []byte("Error: Invalid message format"))
			if err != nil {
				return err
			}
			continue
		}

		// Validate and forward message
		targetId, ok := payload["targetId"].(string)
		if !ok || targetId == "" {
			log.Printf("Missing or invalid targetId in message from %s", peerId)
			err := conn.WriteMessage(websocket.TextMessage, []byte("Error: Missing or invalid targetId"))
			if err != nil {
				return err
			}
			continue
		}

		if targetPeer, ok := Peers.Load(targetId); ok {
			targetConn := targetPeer.(PeerConnection).Conn
			if err := targetConn.WriteMessage(websocket.TextMessage, msg); err != nil {
				log.Printf("Error forwarding message to %s: %v", targetId, err)
			}
		} else {
			log.Printf("Target peer not found: %s", targetId)
			err := conn.WriteMessage(websocket.TextMessage, []byte("Error: Target peer not found"))
			if err != nil {
				return err
			}
		}
	}

	return nil
}
