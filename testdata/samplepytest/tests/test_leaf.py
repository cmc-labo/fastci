from mypkg.leaf import hello
from mypkg.testutil import shout


def test_hello():
    assert hello() == "hello from leaf"
    assert shout("ok") == "OK"
