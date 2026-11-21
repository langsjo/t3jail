package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/langsjo/t3jail/internal/control"
	"github.com/langsjo/t3jail/internal/netrules"
	"github.com/langsjo/t3jail/internal/termsafe"
)

func newNetRequestsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "requests",
		Short: "Answer the VM's requests to reach blocked names",
		Long: `Processes in the VM can ask for a name to be allowed with
` + "`t3jail-request NAME... [REASON]`" + `. This lists the pending requests; on a terminal,
pick for each whether to keep it blocked, allow it until the VM stops, or
allow it permanently. ` + "`t3jail net allow`" + ` also answers matching requests.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inst, err := instance()
			if err != nil {
				return err
			}
			client := control.NewClient(inst.ControlSocket())
			reqs, err := client.NetRequests(cmd.Context())
			if err != nil {
				return err
			}
			if len(reqs) == 0 {
				fmt.Println("No pending requests.")
				return nil
			}
			lines := make([]string, len(reqs))
			for i, r := range reqs {
				// Tabs would break the one-row-per-request layout.
				line := strings.ReplaceAll(fmt.Sprintf("%-30s %s  %s", r.Entry, r.Time.Format("15:04"), r.Reason), "\t", " ")
				lines[i] = string(termsafe.Escape([]byte(line)))
			}
			if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
				fmt.Println(strings.Join(lines, "\n"))
				return nil
			}
			choices, ok, err := pick(os.Stdin, os.Stdout, lines)
			if err != nil {
				return err
			}
			if !ok {
				fmt.Println("Left all requests pending.")
				return nil
			}

			var now, always []netrules.Entry
			denied := map[string]string{}
			for i, r := range reqs {
				e, err := netrules.Parse(r.Entry)
				if err != nil {
					return fmt.Errorf("daemon sent an invalid entry %q: %w", r.Entry, err)
				}
				switch choices[i] {
				case keepBlocked:
					denied[r.Entry] = control.AnswerDenied
				case allowNow:
					now = append(now, e)
				case allowAlways:
					always = append(always, e)
				}
			}
			// Least likely to fail first: an error stops the rest, and
			// allowEntries reports what it did.
			if len(denied) > 0 {
				if err := client.AnswerNetRequests(cmd.Context(), denied); err != nil {
					return err
				}
				fmt.Printf("Kept %d blocked\n", len(denied))
			}
			if len(always) > 0 {
				if err := allowEntries(cmd.Context(), inst, always, true); err != nil {
					return err
				}
			}
			if len(now) > 0 {
				return allowEntries(cmd.Context(), inst, now, false)
			}
			return nil
		},
	}
}

type choice int

const (
	keepBlocked choice = iota
	allowNow
	allowAlways
)

var marks = [...]string{"[ ]", "[✓]", "[P]"}

// picker is the state of the request list; key handling is separate from
// the terminal so it can be tested. The cursor goes one past the last
// request, onto the submit button.
type picker struct {
	choices []choice
	cursor  int
}

func (p *picker) onSubmit() bool { return p.cursor == len(p.choices) }

// key handles one read from the raw terminal (an arrow key's escape sequence
// usually arrives whole; a split one is ignored) and reports whether the user
// is done and wants the choices applied.
func (p *picker) key(k string) (done, apply bool) {
	switch k {
	case "\x1b[A", "k":
		p.cursor = max(p.cursor-1, 0)
	case "\x1b[B", "j":
		p.cursor = min(p.cursor+1, len(p.choices))
	case " ", "\r", "\n":
		if p.onSubmit() {
			return true, true
		}
		p.choices[p.cursor] = (p.choices[p.cursor] + 1) % choice(len(marks))
	case "q", "\x03":
		return true, false
	}
	return false, false
}

var pickHelp = [...]string{
	"↑/↓ move, enter/space cycles, Submit applies (unmarked stay blocked), q leaves all pending",
	"[ ] keep blocked   [✓] allow until the VM stops   [P] allow permanently",
}

// maxPickRows keeps the menu small; longer lists scroll.
const maxPickRows = 10

// pick shows lines with a choice each and returns the choices, or ok=false if
// the user quit.
func pick(in *os.File, out io.Writer, lines []string) (choices []choice, ok bool, err error) {
	fd := int(in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false, err
	}
	defer term.Restore(fd, state)

	// Draw below the prompt rather than on the alternate screen, so the
	// shell's history stays visible. Every row is truncated to the terminal
	// width, so moving up by the number of rows drawn reaches the menu's top.
	drawn := 0
	erase := func() {
		if drawn > 0 {
			fmt.Fprintf(out, "\x1b[%dA", drawn)
		}
		fmt.Fprint(out, "\r\x1b[J")
	}
	fmt.Fprint(out, "\x1b[?25l")
	defer func() {
		erase()
		fmt.Fprint(out, "\x1b[?25h")
	}()

	p := &picker{choices: make([]choice, len(lines))}
	buf := make([]byte, 16)
	for {
		width, height, err := term.GetSize(fd)
		if err != nil || width <= 0 || height <= 0 {
			width, height = 80, 24
		}
		erase()
		var frame strings.Builder
		for _, h := range pickHelp {
			frame.WriteString(truncate(h, width-1) + "\r\n")
		}
		// Show the rows that fit below the help and above the button,
		// keeping the cursor visible.
		rows := max(min(len(lines), maxPickRows, height-len(pickHelp)-2), 1)
		first := max(min(p.cursor, len(lines)-1)-rows+1, 0)
		for i := first; i < first+rows; i++ {
			cur := "  "
			if i == p.cursor {
				cur = "> "
			}
			frame.WriteString(truncate(cur+marks[p.choices[i]]+" "+lines[i], width-1) + "\r\n")
		}
		if p.onSubmit() {
			frame.WriteString("> \x1b[7m[ Submit ]\x1b[27m\r\n")
		} else {
			frame.WriteString("  [ Submit ]\r\n")
		}
		fmt.Fprint(out, frame.String())
		drawn = len(pickHelp) + rows + 1
		n, err := in.Read(buf)
		if err != nil {
			return nil, false, err
		}
		if done, apply := p.key(string(buf[:n])); done {
			return p.choices, apply, nil
		}
	}
}

// truncate keeps lines on one terminal row (wide characters aside).
func truncate(s string, width int) string {
	r := []rune(s)
	if width < 1 || len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}
