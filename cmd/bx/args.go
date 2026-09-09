package main

import "fmt"

// nextArg returns the value that follows the flag at args[*i] and advances
// *i past it. A flag at the end of the line is an error the caller prints —
// never an index panic (`bx org set x --name` once crashed on exactly that).
func nextArg(args []string, i *int) (string, error) {
	if *i+1 >= len(args) {
		return "", fmt.Errorf("%s needs a value", args[*i])
	}
	*i++
	return args[*i], nil
}
