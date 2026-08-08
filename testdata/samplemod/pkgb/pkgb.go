package pkgb

import "samplemod/pkgc"

func Greet() string {
	return pkgc.Hello() + " via b"
}
