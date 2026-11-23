package main

import "os"

func main() {
	a, err := newApp()
	if err != nil {
		os.Stderr.WriteString("error: " + err.Error() + "\n")
		os.Exit(1)
	}
	code := a.run(os.Args[1:])
	// Close the redirected log before exiting: a read-only parent must see a
	// complete file, and os.Exit skips deferred closes.
	a.closeOutput()
	os.Exit(code)
}
