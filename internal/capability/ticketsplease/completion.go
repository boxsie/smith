package ticketsplease

import (
	"fmt"
	"sync"

	"github.com/boxsie/smith/internal/capability"
)

// completionGuard is invocation-local, like the causal facts consumed by the
// harness completion proof. It prevents a bad close before the remote mutation;
// the downstream proof still independently verifies the recorded sequence.
// This is not a remote transaction: another client can change the ticket, and an
// ambiguous transport failure requires a fresh read, never a blind close retry.
type completionGuard struct {
	mu       sync.Mutex
	tickets  map[string]completionState
	auditErr error
}

type completionState struct {
	column    string
	ready     bool
	completed bool
}

func (g *completionGuard) check(ticket string) error {
	if ticket == "" {
		return fmt.Errorf("complete_ticket requires a ticket_id and a successful get_ticket in this invocation")
	}
	state := g.tickets[ticket]
	if state.completed || state.column == "done" {
		return fmt.Errorf("complete_ticket blocked for %q: ticket already completed; do not retry completion", ticket)
	}
	switch state.column {
	case "testing", "todo", "in_progress":
		if state.ready {
			return nil
		}
		return fmt.Errorf("complete_ticket blocked for %q: observed %s; call move_ticket(target_column=testing) with a truthful comment before completing", ticket, state.column)
	default:
		return fmt.Errorf("complete_ticket blocked for %q: no valid current column observation; call get_ticket using the same ticket_id before completing", ticket)
	}
}

func (g *completionGuard) observe(event capability.Event) {
	if event.TicketID == "" {
		return
	}
	state := g.tickets[event.TicketID]
	success := event.Type == "completed"
	switch event.Tool {
	case "get_ticket":
		state.column, state.ready = "", false
		if success {
			state.column = event.Facts["ticket_column"]
			state.ready = state.column == "testing"
		}
	case "move_ticket":
		state.ready = success && event.Facts["target_column"] == "testing" &&
			(state.column == "todo" || state.column == "in_progress" || state.column == "testing")
		// A contradictory response is not evidence of reaching testing.
		if column := event.Facts["ticket_column"]; column != "" && column != "testing" {
			state.ready = false
		}
	case "complete_ticket":
		if success {
			state.completed = true
		}
		state.column, state.ready = "", false
	default:
		return
	}
	g.tickets[event.TicketID] = state
}
