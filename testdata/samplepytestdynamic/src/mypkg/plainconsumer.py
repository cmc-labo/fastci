import mypkg.dynloader as dynloader


def use():
    return dynloader.load("mypkg.leaf")
