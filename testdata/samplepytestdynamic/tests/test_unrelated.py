from mypkg.unrelated import unrelated


def test_unrelated():
    assert unrelated() == "unrelated"
