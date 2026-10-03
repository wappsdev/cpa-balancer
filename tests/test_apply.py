from cpa_balancer.apply import Patch, baseline_patches, patches
from cpa_balancer.models import Choice, Decision
from tests.helpers import FABLE_MODEL, G, W

MANAGED = [W, G]
FABLE = (FABLE_MODEL,)


def decision(main_order, fable_account):
    return Decision(Choice(main_order[0], "r"), Choice(fable_account, "r"), tuple(main_order), ())


def test_priorities_follow_main_order():
    current = {W: {"priority": 10, "excluded_models": None}, G: {"priority": None, "excluded_models": None}}
    out = patches(decision([G, W], G), MANAGED, FABLE, current)
    assert out == [Patch(W, {"priority": 90}), Patch(G, {"priority": 100})]


def test_fable_on_other_account_excludes_it_elsewhere_and_keeps_manual_entries():
    current = {W: {"priority": 90, "excluded_models": ["manual-model"]}, G: {"priority": 100, "excluded_models": None}}
    out = patches(decision([G, W], W), MANAGED, FABLE, current)
    assert out == [Patch(G, {"excluded_models": [FABLE_MODEL]})]


def test_fable_exclusions_removed_when_fable_follows_main():
    current = {W: {"priority": 90, "excluded_models": None},
               G: {"priority": 100, "excluded_models": ["manual-model", FABLE_MODEL]}}
    out = patches(decision([G, W], G), MANAGED, FABLE, current)
    assert out == [Patch(G, {"excluded_models": ["manual-model"]})]


def test_no_patch_when_nothing_changed():
    current = {W: {"priority": 90, "excluded_models": None}, G: {"priority": 100, "excluded_models": None}}
    assert patches(decision([G, W], None), MANAGED, FABLE, current) == []


def test_baseline_restores_priorities_and_drops_fable_exclusions():
    current = {W: {"priority": 90, "excluded_models": [FABLE_MODEL]}, G: {"priority": 100, "excluded_models": None}}
    out = baseline_patches({W: 10, G: 0}, FABLE, current)
    assert out == [Patch(W, {"priority": 10, "excluded_models": None}), Patch(G, {"priority": 0})]
