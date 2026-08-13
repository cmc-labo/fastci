#[test]
fn test_run() {
    assert_eq!(consumer::run(), "hello from leaf via mid via consumer");
    assert_eq!(testutil::shout("ok"), "OK");
}
