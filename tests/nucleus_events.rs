use mitos::events::RuntimeEvent;
use mitos::nucleus::parse_nucleus_jsonl;

#[test]
fn nucleus_jsonl_maps_to_runtime_events() {
    let events = parse_nucleus_jsonl(include_str!("../fixtures/nucleus/events.jsonl")).unwrap();

    assert_eq!(events.len(), 7);
    assert!(matches!(events[0], RuntimeEvent::SandboxPrepared(_)));
    assert!(matches!(events[1], RuntimeEvent::SandboxEvent(_)));
    assert!(matches!(events[4], RuntimeEvent::ResourceSample(_)));

    let serialized = events
        .iter()
        .map(serde_json::to_string)
        .collect::<Result<Vec<_>, _>>()
        .unwrap()
        .join("\n")
        + "\n";

    assert_eq!(
        serialized,
        include_str!("../fixtures/nucleus/runtime_events.jsonl")
    );
}
