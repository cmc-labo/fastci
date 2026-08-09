package pkgconsumer

import "wsleaf/pkgleaf"

func Greet() string {
	return pkgleaf.Hello() + " via consumer"
}
