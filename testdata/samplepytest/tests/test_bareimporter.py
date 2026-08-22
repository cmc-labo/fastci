from mypkg.bareimporter import greet_bare


def test_greet_bare():
    assert greet_bare() == "hello from leaf"
