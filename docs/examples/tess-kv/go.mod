module example.com/tess-kv

go 1.26.7

require github.com/arunksoman/tessera v0.0.0

// Builds against the tessera code in this commit. A plugin of your own requires a released
// version instead: go get github.com/arunksoman/tessera@latest
replace github.com/arunksoman/tessera => ../../..
