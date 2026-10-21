package pkguser

import "embedmod/pkgtmpl"

func Run() string { return pkgtmpl.Greeting() + " via user" }
