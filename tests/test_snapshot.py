from datetime import timedelta

from cpa_balancer.models import FABLE, MAIN, SESSION, Reading
from cpa_balancer.snapshot import build_snapshot, header_readings, merge, roll_forward
from tests.helpers import FABLE_MODEL, MAIN_MODEL, NOW, W, cred, reading


def test_header_readings_split_buckets_and_convert_to_percent():
    credential = cred(W, main=35, main_reset_h=100, fable=1, fable_reset_h=100, session=7, session_reset_h=3)
    out = header_readings(credential, (MAIN_MODEL,), (FABLE_MODEL,))
    assert round(out[MAIN].util, 6) == 35
    assert round(out[FABLE].util, 6) == 1
    assert round(out[SESSION].util, 6) == 7
    assert out[MAIN].resets_at == NOW + timedelta(hours=100)
    assert out[MAIN].source == "header"


def test_header_readings_ignore_credential_level_quota_for_weekly_buckets():
    credential = cred(W, main=35)
    credential["quota"] = {
        "observed_at": NOW.isoformat(),
        "signals": {"Anthropic-Ratelimit-Unified-7d-Utilization": "0.99",
                    "Anthropic-Ratelimit-Unified-7d-Reset": str(int(NOW.timestamp()) + 3600),
                    "Anthropic-Ratelimit-Unified-5h-Utilization": "0.5",
                    "Anthropic-Ratelimit-Unified-5h-Reset": str(int(NOW.timestamp()) + 3600)},
    }
    out = header_readings(credential, (MAIN_MODEL,), (FABLE_MODEL,))
    assert round(out[MAIN].util, 6) == 35          # not 99 from the credential-level entry
    assert round(out[SESSION].util, 6) == 50        # 5h is shared, newest entry wins


def test_merge_keeps_newest_reading_per_bucket():
    old = {MAIN: reading(10, 50, age_min=30)}
    new = {MAIN: reading(20, 50, age_min=5), FABLE: reading(1, 50)}
    out = merge(old, new)
    assert out[MAIN].util == 20
    assert out[FABLE].util == 1


def test_roll_forward_empties_a_window_whose_reset_passed():
    passed = Reading(80, NOW - timedelta(hours=1), NOW - timedelta(days=2), "probe")
    out = roll_forward({MAIN: passed, SESSION: reading(5, 2)}, NOW)
    assert out[MAIN].util == 0
    assert out[MAIN].source == "estimate"
    assert out[MAIN].resets_at == NOW - timedelta(hours=1) + timedelta(days=7)
    assert out[SESSION].util == 5


def test_build_snapshot_merges_probe_data_and_cooldowns():
    credential = cred(W, main=35, age_min=90, cooldowns=[MAIN_MODEL], disabled=True)
    snapshot = build_snapshot(credential, {MAIN: reading(58, 100, age_min=2)}, (MAIN_MODEL,), (FABLE_MODEL,), NOW)
    assert snapshot.auth_file == W
    assert snapshot.readings[MAIN].util == 58     # probe is newer than the 90-min-old header
    assert snapshot.cooling_models == frozenset({MAIN_MODEL})
    assert snapshot.unavailable is True


def test_passed_session_window_becomes_closed():
    passed = Reading(40, NOW - timedelta(minutes=1), NOW - timedelta(hours=2), "header")
    out = roll_forward({SESSION: passed}, NOW)
    assert out[SESSION].util == 0
    assert out[SESSION].resets_at is None
    assert out[SESSION].source == "estimate"


def test_closed_session_reading_is_kept_as_is():
    closed = Reading(0, None, NOW - timedelta(minutes=3), "probe")
    assert roll_forward({SESSION: closed}, NOW)[SESSION] == closed
