from datetime import timedelta
from pathlib import Path

from cpa_balancer.config import Thresholds
from cpa_balancer.models import MAIN
from cpa_balancer.policy import cap, decide
from tests.helpers import G, MAIN_MODEL, NOW, W, make_config, reading, snap

CFG = make_config(Path("/tmp/cpa-balancer-tests"))
TH = Thresholds()


def run(snaps, active=None, last_order=()):
    return decide({s.auth_file: s for s in snaps}, CFG, NOW, active or {}, last_order)


def test_cap_tiers_at_exact_boundaries():
    assert cap(True, NOW + timedelta(hours=48), NOW, TH) == 85
    assert cap(True, NOW + timedelta(hours=48) - timedelta(seconds=1), NOW, TH) == 90
    assert cap(True, NOW + timedelta(hours=12), NOW, TH) == 90
    assert cap(True, NOW + timedelta(hours=12) - timedelta(seconds=1), NOW, TH) == 99
    assert cap(False, NOW + timedelta(hours=150), NOW, TH) == 99


def test_earliest_reset_wins_and_others_follow_in_reset_order():
    d = run([snap(W, main=reading(10, 140)), snap(G, main=reading(52, 90))])
    assert d.main.account == G
    assert d.main_order == (G, W)
    assert d.main.relaxed is False


def test_reserve_cap_forces_switch_to_next_account():
    d = run([snap(W, main=reading(10, 140)), snap(G, main=reading(86, 90))],
            active={MAIN: (G, NOW - timedelta(minutes=5))})
    assert d.main.account == W
    assert "forced switch" in d.main.reason


def test_near_reset_raises_reserve_cap_to_90_then_99():
    assert run([snap(W, main=reading(10, 140)), snap(G, main=reading(86, 30))]).main.account == G
    assert run([snap(W, main=reading(10, 140)), snap(G, main=reading(98, 6))]).main.account == G


def test_session_cap_switches_and_returns_after_reset():
    d = run([snap(W, main=reading(10, 140)), snap(G, main=reading(52, 90), session=reading(98, 1))],
            active={MAIN: (G, NOW - timedelta(minutes=5))})
    assert d.main.account == W
    back = run([snap(W, main=reading(10, 140)), snap(G, main=reading(52, 90), session=reading(0, 5))],
               active={MAIN: (W, NOW - timedelta(minutes=61))})
    assert back.main.account == G


def test_preferred_switch_waits_for_dwell():
    snaps = [snap(W, main=reading(10, 140)), snap(G, main=reading(52, 90))]
    stay = run(snaps, active={MAIN: (W, NOW - timedelta(minutes=30))})
    assert stay.main.account == W
    assert "dwell" in stay.main.reason
    moved = run(snaps, active={MAIN: (W, NOW - timedelta(minutes=60))})
    assert moved.main.account == G


def test_reserves_released_when_nothing_else_is_eligible():
    d = run([snap(W, main=reading(99.5, 140)), snap(G, main=reading(86, 90))])
    assert d.main.account == G
    assert d.main.relaxed is True
    assert any(w.startswith("main:") for w in d.warnings)


def test_no_restriction_when_everything_is_exhausted():
    d = run([snap(W, main=reading(99.5, 140)), snap(G, main=reading(99.5, 90))], last_order=(W, G))
    assert d.main.account is None
    assert d.main_order == (W, G)
    assert d.warnings


def test_fable_is_decided_independently():
    d = run([snap(W, main=reading(10, 140), fable=reading(1, 140)),
             snap(G, main=reading(52, 90), fable=reading(99.5, 90))])
    assert d.main.account == G
    assert d.fable.account == W


def test_stale_data_is_never_chosen_but_kept_as_fallback():
    d = run([snap(W, main=reading(10, 140)), snap(G, main=reading(52, 90, age_min=7 * 60))])
    assert d.main.account == W
    assert d.main_order == (W, G)


def test_cooldown_in_cpa_blocks_only_that_bucket():
    d = run([snap(W, main=reading(10, 140), fable=reading(1, 140)),
             snap(G, main=reading(52, 90), fable=reading(1, 90), cooling=[MAIN_MODEL])])
    assert d.main.account == W
    assert d.fable.account == G


def test_disabled_account_is_not_chosen():
    d = run([snap(W, main=reading(10, 140)), snap(G, main=reading(52, 90), unavailable=True)])
    assert d.main.account == W


from dataclasses import replace  # noqa: E402

from cpa_balancer.config import AnchorConfig  # noqa: E402
from cpa_balancer.models import SESSION, Reading  # noqa: E402
from cpa_balancer.policy import anchor_did_not_start_window, anchor_targets  # noqa: E402

CLOSED = Reading(0, None, NOW, "probe")


def targets(snaps, last=None, retry=None, cfg=CFG):
    by_name = {s.auth_file: s for s in snaps}
    decision = decide(by_name, cfg, NOW, {}, ())
    return anchor_targets(decision, by_name, cfg, NOW, last or {}, retry or {})


ACTIVE_G = snap(G, main=reading(52, 90), session=reading(30, 3))


def test_anchors_idle_account_while_active_window_runs():
    assert targets([ACTIVE_G, snap(W, main=reading(10, 140))]) == [W]
    assert targets([ACTIVE_G, snap(W, main=reading(10, 140), session=CLOSED)]) == [W]


def test_no_anchor_when_other_window_already_runs():
    assert targets([ACTIVE_G, snap(W, main=reading(10, 140), session=reading(5, 2))]) == []


def test_no_anchor_without_work_on_the_active_account():
    assert targets([snap(G, main=reading(52, 90), session=CLOSED), snap(W, main=reading(10, 140))]) == []
    assert targets([snap(G, main=reading(52, 90)), snap(W, main=reading(10, 140))]) == []


def test_no_anchor_on_weekly_exhausted_account():
    assert targets([ACTIVE_G, snap(W, main=reading(99.5, 140))]) == []


def test_anchor_spacing_and_retry_after():
    idle = snap(W, main=reading(10, 140))
    assert targets([ACTIVE_G, idle], last={W: NOW - timedelta(minutes=9)}) == []
    assert targets([ACTIVE_G, idle], last={W: NOW - timedelta(minutes=10)}) == [W]
    assert targets([ACTIVE_G, idle], retry={W: NOW + timedelta(minutes=1)}) == []


def test_anchor_disabled_in_config():
    off = replace(CFG, anchor=AnchorConfig(enabled=False))
    assert targets([ACTIVE_G, snap(W, main=reading(10, 140))], cfg=off) == []


def test_detects_that_haiku_did_not_start_the_window():
    anchored = Reading(0, NOW + timedelta(hours=3), NOW - timedelta(hours=2), "anchor")
    assert anchor_did_not_start_window(CLOSED, anchored, NOW - timedelta(hours=2)) is True
    assert anchor_did_not_start_window(reading(1, 3, age_min=0), anchored, NOW - timedelta(hours=2)) is False
    expired = Reading(0, NOW - timedelta(minutes=1), NOW - timedelta(hours=5), "anchor")
    assert anchor_did_not_start_window(CLOSED, expired, NOW - timedelta(hours=5)) is False
    assert anchor_did_not_start_window(CLOSED, None, None) is False
