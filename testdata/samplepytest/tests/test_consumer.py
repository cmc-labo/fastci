from mypkg.sub.consumer import run


def test_run():
    assert run() == "hello from leaf via mid via consumer"
