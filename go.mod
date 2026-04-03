module github.com/dan-strohschein/chisel

go 1.25.5

require github.com/dan-strohschein/cartograph v0.2.0

require github.com/dan-strohschein/aidkit v0.1.0

replace (
	github.com/dan-strohschein/aidkit => ../AID/tools/aidkit
	github.com/dan-strohschein/cartograph => ../cartograph
)
