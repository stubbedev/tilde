module github.com/stubbedev/lys-wm

go 1.27.3

require (
	github.com/ebitengine/purego v0.11.1
	github.com/godbus/dbus/v5 v5.2.4
	github.com/jezek/xgb v1.3.1
	github.com/stubbedev/lys-ui v0.0.0-20261024131436-04e266d1d635
	go.yaml.in/yaml/v3 v3.0.5
	golang.org/x/sys v0.49.0
)

require (
	github.com/go-text/typesetting v0.3.5 // indirect
	github.com/kettek/apng v0.0.0-20250827064933-2bb5f5fcf253 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/srwiley/oksvg v0.0.0-20221011165216-be6e8873101c // indirect
	github.com/srwiley/rasterx v0.0.0-20220730225603-2ab79fcdd4ef // indirect
	golang.org/x/image v0.46.0 // indirect
	golang.org/x/net v0.60.0 // indirect
	golang.org/x/text v0.42.0 // indirect
)

replace github.com/jezek/xgb => ./third_party/xgb
