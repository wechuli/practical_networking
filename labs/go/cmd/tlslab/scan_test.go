package main

import "fmt"

func fmtSscanf(s string, f *float64) (int, error) { return fmt.Sscanf(s, "%f", f) }
