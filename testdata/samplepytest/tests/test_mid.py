from mypkg.mid import greet


def test_greet():
    assert greet() == "hello from leaf via mid"
