// Debug tool to inspect Mumble server channels and test channel joining.
package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"time"

	"layeh.com/gumble/gumble"
	"layeh.com/gumble/gumbleutil"
)

func main() {
	gConfig := gumble.NewConfig()
	gConfig.Username = "DebugBot"
	gConfig.Password = "test"
	gConfig.Attach(gumbleutil.AutoBitrate)

	gConfig.Attach(gumbleutil.Listener{
		Connect: func(e *gumble.ConnectEvent) {
			log.Printf("=== CONNECTED ===")
			log.Printf("Self channel: %v", e.Client.Self.Channel)
			if e.Client.Self.Channel != nil {
				log.Printf("Self is in channel: %q (ID=%d)", e.Client.Self.Channel.Name, e.Client.Self.Channel.ID)
			}
			log.Printf("Total channels on server: %d", len(e.Client.Channels))
			for id, ch := range e.Client.Channels {
				parenth := ""
				if ch.Parent != nil {
					parenth = fmt.Sprintf(" parent=%q(ID=%d)", ch.Parent.Name, ch.Parent.ID)
				}
				log.Printf("  Channel ID=%d: %q (root=%v, temp=%v)%s", id, ch.Name, ch.IsRoot(), ch.Temporary, parenth)
			}
		},
		Disconnect: func(e *gumble.DisconnectEvent) {
			log.Printf("Disconnected: %v", e.String)
		},
	})

	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
	}

	client, err := gumble.DialWithDialer(new(net.Dialer), "127.0.0.1:64738", gConfig, tlsConfig)
	if err != nil {
		log.Fatalf("Dial failed: %v", err)
	}

	time.Sleep(2 * time.Second)

	// Try to join "Test" channel
	log.Printf("\n=== Attempting to join channel 'Test' ===")
	ch := client.Channels.Find("Test")
	if ch == nil {
		log.Printf("Channel 'Test' NOT FOUND via Find()")
	} else {
		log.Printf("Found 'Test': ID=%d, name=%q", ch.ID, ch.Name)
		log.Printf("Before Move: Self.Channel = %q (ID=%d)", client.Self.Channel.Name, client.Self.Channel.ID)
		client.Self.Move(ch)
		log.Printf("Sent Move command to Test channel")
		time.Sleep(1 * time.Second)
		log.Printf("After Move: Self.Channel = %q (ID=%d)", client.Self.Channel.Name, client.Self.Channel.ID)
	}

	// Try creating a "Music" channel
	log.Printf("\n=== Creating channel 'Music' ===")
	root := client.Channels[0]
	if root != nil {
		log.Printf("Root channel: %q (ID=%d)", root.Name, root.ID)
		root.Add("Music", false)
		log.Printf("Sent Add command for 'Music'")
		time.Sleep(1 * time.Second)
		log.Printf("Channels after create: %d", len(client.Channels))
		for id, ch := range client.Channels {
			log.Printf("  ID=%d: %q", id, ch.Name)
		}
	}

	// Try joining the newly created "Music" channel
	log.Printf("\n=== Attempting to join newly created 'Music' ===")
	musicCh := client.Channels.Find("Music")
	if musicCh == nil {
		log.Printf("Channel 'Music' NOT FOUND after creation")
	} else {
		log.Printf("Found 'Music': ID=%d", musicCh.ID)
		client.Self.Move(musicCh)
		time.Sleep(1 * time.Second)
		log.Printf("Self channel after move: %q (ID=%d)", client.Self.Channel.Name, client.Self.Channel.ID)
	}

	client.Disconnect()
	log.Printf("Done")
}
