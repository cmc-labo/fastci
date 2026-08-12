from mypkg.isolated import isolated


def test_isolated():
    assert isolated() == "isolated"
