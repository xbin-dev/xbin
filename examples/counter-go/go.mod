module counter

go 1.24

require github.com/magik6k/buxon/sdk v0.0.0

// Resolved by the workspace go.work under buxond (bx doctor checks this);
// this replace keeps the example buildable straight from the buxon repo too.
replace github.com/magik6k/buxon/sdk => ../../sdk
