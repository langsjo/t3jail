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
` + "`t3jail-request NAME [REASON]`" + `. This lists the pending requests; on a terminal,
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
// the terminal so it can be tested.
type picker struct {
	choices []choice
	cursor  int
}

// key handles one read from the raw terminal (an arrow key's escape sequence
// usually arrives whole; a split one is ignored) and reports whether the user
// is done and wants the choices applied.
func (p *picker) key(k string) (done, apply bool) {
	switch k {
	case "\x1b[A", "k":
		p.cursor = max(p.cursor-1, 0)
	case "\x1b[B", "j":
		p.cursor = min(p.cursor+1, len(p.choices)-1)
	case " ":
		p.choices[p.cursor] = (p.choices[p.cursor] + 1) % choice(len(marks))
	case "\r", "\n":
		return true, true
	case "q", "\x03":
		return true, false
	}
	return false, false
}

const pickHelp = "↑/↓ move, space cycles, enter applies (unmarked stay blocked), q quits and leaves all pending\r\n" +
	"[ ] keep blocked   [✓] allow until the VM stops   [P] allow permanently\r\n"

// pick shows lines with a choice each and returns the choices, or ok=false if
// the user quit.
func pick(in *os.File, out io.Writer, lines []string) (choices []choice, ok bool, err error) {
	fd := int(in.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		return nil, false, err
	}
	defer term.Restore(fd, state)
	// Draw on the alternate screen, from the top each time, so nothing
	// depends on how many rows the previous frame took.
	fmt.Fprint(out, "\x1b[?1049h")
	defer fmt.Fprint(out, "\x1b[?1049l")

	p := &picker{choices: make([]choice, len(lines))}
	buf := make([]byte, 16)
	for {
		width, height, err := term.GetSize(fd)
		if err != nil {
			width, height = 80, 24
		}
		fmt.Fprint(out, "\x1b[H\x1b[2J"+pickHelp)
		// Show the rows that fit, keeping the cursor visible.
		rows := max(height-3, 1)
		first := max(p.cursor-rows+1, 0)
		for i := first; i < min(first+rows, len(lines)); i++ {
			cur := "  "
			if i == p.cursor {
				cur = "> "
			}
			fmt.Fprintf(out, "%s\r\n", truncate(cur+marks[p.choices[i]]+" "+lines[i], width-1))
		}
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
