package gmcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func waitNotification(t *testing.T, sub *ClientSubscription) Notification {
	t.Helper()
	select {
	case notification, ok := <-sub.Notifications():
		if !ok {
			t.Fatalf("subscription closed: %v", sub.Err())
		}
		return notification
	case <-time.After(2 * time.Second):
		t.Fatal("no notification arrived")
		return Notification{}
	}
}

func TestSubscriptionAcknowledgesThenDelivers(t *testing.T) {
	server := newTestServer(t)
	server.SetSubscriptionHandler(func(sub *Subscription) { <-sub.Done() })

	client, _, cleanup := startPair(t, server)
	defer cleanup()

	ctx := context.Background()
	if _, err := client.Connect(ctx, Options{Info: ClientInfo{Name: "subscriber"}}); err != nil {
		t.Fatal(err)
	}

	sub, err := client.Listen(ctx, SubscriptionFilter{ToolsListChanged: true})
	if err != nil {
		t.Fatal(err)
	}
	if !sub.Filter().ToolsListChanged {
		t.Fatalf("granted filter = %+v", sub.Filter())
	}

	server.ToolsChanged()
	notification := waitNotification(t, sub)

	if notification.Method != NotifyToolsListChanged {
		t.Fatalf("method = %q", notification.Method)
	}
	var envelope struct {
		Meta struct {
			SubscriptionID json.RawMessage `json:"io.modelcontextprotocol/subscriptionId"`
		} `json:"_meta"`
	}
	if err := json.Unmarshal(notification.Params, &envelope); err != nil {
		t.Fatal(err)
	}
	if string(envelope.Meta.SubscriptionID) != string(sub.ID()) {
		t.Fatalf("subscriptionId = %s, want %s", envelope.Meta.SubscriptionID, sub.ID())
	}

	if err := sub.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not end")
	}
}

func TestSubscriptionRejectsUnrequestedTypes(t *testing.T) {
	server := newTestServer(t)
	server.SetSubscriptionHandler(func(sub *Subscription) { <-sub.Done() })

	client, _, cleanup := startPair(t, server)
	defer cleanup()

	ctx := context.Background()
	if _, err := client.Connect(ctx, Options{Info: ClientInfo{Name: "subscriber"}}); err != nil {
		t.Fatal(err)
	}

	sub, err := client.Listen(ctx, SubscriptionFilter{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sub.Close() }()

	if sub.Filter().ToolsListChanged {
		t.Fatalf("granted filter = %+v", sub.Filter())
	}
	server.ToolsChanged()
	select {
	case notification := <-sub.Notifications():
		t.Fatalf("unexpected notification %+v", notification)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestSubscriptionFinishIsGraceful(t *testing.T) {
	server := newTestServer(t)
	server.SetSubscriptionHandler(func(sub *Subscription) { sub.Finish() })

	client, _, cleanup := startPair(t, server)
	defer cleanup()

	ctx := context.Background()
	if _, err := client.Connect(ctx, Options{Info: ClientInfo{Name: "subscriber"}}); err != nil {
		t.Fatal(err)
	}

	sub, err := client.Listen(ctx, SubscriptionFilter{ToolsListChanged: true})
	if err != nil {
		t.Fatal(err)
	}

	select {
	case <-sub.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("subscription did not end")
	}
	if err := sub.Err(); err != nil {
		t.Fatalf("graceful end reported %v", err)
	}
}

func TestSubscriptionRequiresModernConnection(t *testing.T) {
	client, _, cleanup := startPair(t, newTestServer(t))
	defer cleanup()

	if _, err := client.Listen(context.Background(), SubscriptionFilter{ToolsListChanged: true}); err == nil {
		t.Fatal("expected a legacy connection to refuse subscriptions")
	}
}
