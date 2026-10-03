from datetime import timedelta

from cpa_balancer.models import MAIN
from cpa_balancer.state import ProbeState, State, load_state, save_state
from tests.helpers import G, NOW, W, reading


def test_missing_file_gives_empty_state(tmp_path):
    state = load_state(tmp_path / "nope.json")
    assert state.active == {} and state.probes == {} and state.disabled is False


def test_round_trip(tmp_path):
    path = tmp_path / "sub" / "state.json"
    state = State(
        active={MAIN: (G, NOW), "fable": (None, None)},
        probes={W: ProbeState(NOW, NOW + timedelta(minutes=5), {MAIN: reading(58, 100)})},
        last_main_order=(G, W),
        disabled=True,
    )
    save_state(state, path)
    assert load_state(path) == state
    assert not path.with_suffix(".tmp").exists()
