// The demonstration module for the robustness chain. It is a fixture: three
// packages with three declared couplings, and two tests, one normal range and
// one sweep, so that chain_test.go in the covdiff package can run the four
// commands over it and assert the three-column reading.
module demo

go 1.23.0

require github.com/dd0wney/fault v0.0.0

replace github.com/dd0wney/fault => ../../../..
