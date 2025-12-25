module github.com/bogdanfinn/fhttp

go 1.24.1

toolchain go1.24.4

require (
	github.com/andybalholm/brotli v1.1.1
	github.com/bogdanfinn/utls v1.7.5-barnius
	github.com/klauspost/compress v1.17.11
	golang.org/x/net v0.38.0
	golang.org/x/term v0.30.0
)

require (
	golang.org/x/crypto v0.36.0 // indirect
	golang.org/x/sys v0.31.0 // indirect
	golang.org/x/text v0.23.0 // indirect
)

// replace github.com/bogdanfinn/utls => ../utls
