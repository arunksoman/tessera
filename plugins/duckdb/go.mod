module github.com/arunksoman/tessera/plugins/duckdb

go 1.26.7

toolchain go1.26.11

require (
	github.com/arunksoman/tessera v0.0.0
	github.com/duckdb/duckdb-go/v2 v2.20001.0
)

require (
	github.com/apache/arrow-go/v18 v18.5.2 // indirect
	github.com/duckdb/duckdb-go-bindings v0.20001.0 // indirect
	github.com/go-viper/mapstructure/v2 v2.5.0 // indirect
	github.com/goccy/go-json v0.12.0 // indirect
	github.com/google/flatbuffers v25.12.19+incompatible // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/klauspost/compress v1.19.2 // indirect
	github.com/klauspost/cpuid/v2 v2.4.2 // indirect
	github.com/pierrec/lz4/v4 v4.1.33 // indirect
	github.com/zeebo/xxh3 v1.1.0 // indirect
	go.duckdb.org/libduckdb/darwin-amd64 v0.20001.0 // indirect
	go.duckdb.org/libduckdb/darwin-arm64 v0.20001.0 // indirect
	go.duckdb.org/libduckdb/linux-amd64 v0.20001.0 // indirect
	go.duckdb.org/libduckdb/linux-arm64 v0.20001.0 // indirect
	go.duckdb.org/libduckdb/windows-amd64 v0.20001.0 // indirect
	golang.org/x/exp v0.0.0-20260112195511-716be5621a96 // indirect
	golang.org/x/mod v0.33.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/sys v0.50.0 // indirect
	golang.org/x/telemetry v0.0.0-20260209163413-e7419c687ee4 // indirect
	golang.org/x/tools v0.42.0 // indirect
	golang.org/x/xerrors v0.0.0-20240903120638-7835f813f4da // indirect
)

// The plugin always builds against the tessera code in the same commit.
replace github.com/arunksoman/tessera => ../..
