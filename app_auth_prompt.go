package main

// Interactive SSH auth prompts: a username prompt when a hop has none
// configured, and a keyboard-interactive / password fallback when the
// configured methods fail or the server demands it (PAM 2FA). Both reuse the
// host-key challenge plumbing (gotcha 9): register a channel, emit an event,
// block on the channel with a 2-minute timeout defaulting to cancel. The ssh
// layer reaches these through package-var hooks (sshlayer.UsernamePromptHook /
// InteractiveAuthHook) so it stays decoupled from the IPC.

import (
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"

	sshlayer "ssh-tool/internal/ssh"
)

// authPromptReply is what the frontend returns for one prompt exchange.
// cancelled=true (or an empty answers set on a required prompt) aborts.
type authPromptReply struct {
	answers   []string
	cancelled bool
}

// promptTimeout bounds how long a connect waits for the user before aborting,
// matching the host-key challenge timeout.
const promptTimeout = 2 * time.Minute

// UsernamePromptEvent asks the frontend to collect a username for a hop.
type UsernamePromptEvent struct {
	PromptID string `json:"prompt_id"`
	Label    string `json:"label"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
}

// AuthPromptQuestion is one server-issued challenge line.
type AuthPromptQuestion struct {
	Echo bool   `json:"echo"`
	Text string `json:"text"`
}

// AuthPromptEvent asks the frontend to answer a server's keyboard-interactive
// (or plain password) challenge.
type AuthPromptEvent struct {
	PromptID    string               `json:"prompt_id"`
	Label       string               `json:"label"`
	Host        string               `json:"host"`
	Port        int                  `json:"port"`
	Name        string               `json:"name"`        // server-provided title
	Instruction string               `json:"instruction"` // server-provided instruction
	Questions   []AuthPromptQuestion `json:"questions"`
}

// initAuthPrompts wires the ssh-layer hooks to the IPC-backed implementations.
func (a *App) initAuthPrompts() {
	sshlayer.UsernamePromptHook = a.promptUsername
	sshlayer.InteractiveAuthHook = a.promptInteractiveAuth
}

// registerPrompt allocates a pending-prompt channel and returns its id.
func (a *App) registerPrompt() (string, chan authPromptReply) {
	id := uuid.New().String()
	ch := make(chan authPromptReply, 1)
	a.pendingAuthPromptsMu.Lock()
	a.pendingAuthPrompts[id] = ch
	a.pendingAuthPromptsMu.Unlock()
	return id, ch
}

func (a *App) unregisterPrompt(id string) {
	a.pendingAuthPromptsMu.Lock()
	delete(a.pendingAuthPrompts, id)
	a.pendingAuthPromptsMu.Unlock()
}

// waitPrompt blocks for the frontend reply, the app shutting down, or the
// timeout. The last two abort the connect.
func (a *App) waitPrompt(id string, ch chan authPromptReply) (authPromptReply, error) {
	select {
	case r := <-ch:
		if r.cancelled {
			return r, fmt.Errorf("cancelled by user")
		}
		return r, nil
	case <-a.ctx.Done():
		return authPromptReply{}, fmt.Errorf("app is shutting down")
	case <-time.After(promptTimeout):
		log.Printf("auth prompt %s timed out after %s; aborting", id, promptTimeout)
		return authPromptReply{}, fmt.Errorf("prompt timed out")
	}
}

// promptUsername backs sshlayer.UsernamePromptHook.
func (a *App) promptUsername(label, host string, port int) (string, error) {
	id, ch := a.registerPrompt()
	defer a.unregisterPrompt(id)

	EventsEmit("username_prompt", UsernamePromptEvent{
		PromptID: id, Label: label, Host: host, Port: port,
	})
	a.RequestAttention()

	r, err := a.waitPrompt(id, ch)
	if err != nil {
		return "", err
	}
	if len(r.answers) == 0 {
		return "", fmt.Errorf("no username provided")
	}
	return r.answers[0], nil
}

// promptInteractiveAuth backs sshlayer.InteractiveAuthHook.
func (a *App) promptInteractiveAuth(label, host string, port int, name, instruction string, prompts []sshlayer.InteractiveAuthPrompt) ([]string, error) {
	id, ch := a.registerPrompt()
	defer a.unregisterPrompt(id)

	qs := make([]AuthPromptQuestion, len(prompts))
	for i, p := range prompts {
		qs[i] = AuthPromptQuestion{Echo: p.Echo, Text: p.Text}
	}
	EventsEmit("auth_prompt", AuthPromptEvent{
		PromptID: id, Label: label, Host: host, Port: port,
		Name: name, Instruction: instruction, Questions: qs,
	})
	a.RequestAttention()

	r, err := a.waitPrompt(id, ch)
	if err != nil {
		return nil, err
	}
	return r.answers, nil
}

// SshRespondAuthPrompt is called by the frontend to answer a username or
// keyboard-interactive prompt. cancel=true (or a nil answers slice on cancel)
// aborts the connect.
func (a *App) SshRespondAuthPrompt(promptID string, answers []string, cancel bool) error {
	a.pendingAuthPromptsMu.Lock()
	ch, ok := a.pendingAuthPrompts[promptID]
	a.pendingAuthPromptsMu.Unlock()
	if !ok {
		return fmt.Errorf("unknown prompt %s", promptID)
	}
	ch <- authPromptReply{answers: answers, cancelled: cancel}
	return nil
}
