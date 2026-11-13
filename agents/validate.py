#!/usr/bin/env python3
"""Check authored library schemas, references and content integrity; no registration."""
import hashlib
import json
from pathlib import Path
import sys

from jsonschema import Draft202012Validator
from jsonschema.exceptions import ValidationError

ROOT = Path(__file__).resolve().parent


def load(path):
    def unique(pairs):
        result = {}
        for key, value in pairs:
            if key in result:
                raise ValueError('Duplicate field: ' + key)
            result[key] = value
        return result
    return json.loads(path.read_text(encoding='utf-8'), object_pairs_hook=unique)


def local(base, name):
    candidate = (base / name).resolve()
    if not candidate.is_relative_to(base.resolve()):
        raise ValueError('Package path escape: ' + name)
    return candidate


def validate():
    schema = load(ROOT / 'definition.schema.json')
    Draft202012Validator.check_schema(schema)
    validator = Draft202012Validator(schema)
    bundle = load(ROOT / 'defaults/library.json')
    assert set(bundle) == {'profile', 'id', 'revision', 'agents'}
    assert bundle['profile'] == 'agent-library-bundle/v1'
    assert bundle['id'] == 'mitos.defaults' and bundle['revision'] == 2
    assert 0 < len(bundle['agents']) <= 128
    identities, listed = set(), set()
    for entry in bundle['agents']:
        assert set(entry) == {'id', 'revision', 'path'}
        path = local(ROOT / 'defaults', entry['path'])
        assert path.name == 'agent.json' and path.stat().st_size <= 65536
        data = load(path)
        validator.validate(data)
        identity = (data['id'], data['revision'])
        assert identity == (entry['id'], entry['revision']) and identity not in identities
        identities.add(identity)
        listed.add(path)
        instructions = local(path.parent, data['instructions']['path']).read_bytes()
        assert 0 < len(instructions) <= 65536
        instructions.decode('utf-8')
        assert hashlib.sha256(instructions).hexdigest() == data['instructions']['sha256']
        for section in ['context', 'capabilities']:
            assert not set(data[section]['required']) & set(data[section]['optional'])
        assert data['specialization']['max_instruction_bytes'] <= 65536
        for dependency in data['skills'] + data['knowledge']:
            assert dependency['id'] and dependency['sha256']
    assert listed == {p.resolve() for p in (ROOT / 'defaults').glob('*/agent.json')}
    assert ('mitos.generalist', 1) in identities and ('mitos.planner', 2) in identities
    print('Validated ' + str(len(identities)) + ' definitions, bundle references and instruction digests.')


if __name__ == '__main__':
    try:
        validate()
    except (ValueError, AssertionError, OSError, ValidationError) as error:
        sys.exit('Library validation failed: ' + str(error))
