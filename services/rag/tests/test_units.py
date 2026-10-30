import numpy as np

from lumen_rag.answer import NOT_FOUND, ExtractiveAnswerer, cited_chunks, format_sources
from lumen_rag.chunking import chunk_text
from lumen_rag.embeddings import DIM, HashingEmbedder
from lumen_rag.retrieval import FEEDBACK_WEIGHT, Hit, feedback_multiplier, fuse

DOC = """# Billing and plans

## Plans

- **Starter** — free, up to 1 million events per month.
- **Growth** — $499 per month.

## Refunds and cancellation

You can cancel at any time. We refund annual Growth plans pro rata if you cancel within the first 60 days. Monthly plans are not refunded.
"""


def test_chunks_follow_sections_and_carry_heading_path():
    chunks = chunk_text(DOC, max_words=120)
    assert [c.ordinal for c in chunks] == [0, 1]
    assert chunks[0].content.startswith("Billing and plans > Plans\n\n")
    assert "Growth" in chunks[0].content and "$499" in chunks[0].content
    assert chunks[1].content.startswith("Billing and plans > Refunds and cancellation")
    assert "Monthly plans are not refunded." in chunks[1].content


def test_long_paragraphs_split_on_sentences_within_budget():
    para = " ".join(f"Sentence number {i} has exactly seven words." for i in range(40))
    chunks = chunk_text("# T\n\n" + para, max_words=50)
    assert len(chunks) > 4
    for c in chunks:
        body = c.content.split("\n\n", 1)[1]
        assert len(body.split()) <= 50
        assert body.endswith(".")


def test_hashing_embedder_is_deterministic_normalised_and_semantic_enough():
    e = HashingEmbedder()
    v = e.embed(["refund for monthly plans", "refunds on a monthly plan", "SAML single sign-on with Okta"])
    assert v.shape == (3, DIM)
    assert np.allclose(np.linalg.norm(v, axis=1), 1.0, atol=1e-5)
    assert np.allclose(v, e.embed(["refund for monthly plans", "refunds on a monthly plan",
                                   "SAML single sign-on with Okta"]))
    assert v[0] @ v[1] > 0.5 > v[0] @ v[2]


def test_fuse_rewards_agreement_between_retrievers():
    ranked = fuse(vector_ids=[1, 2, 3], keyword_ids=[3, 4, 1])
    ids = [cid for cid, _ in ranked]
    assert ids[:2] == [1, 3]  # in both lists
    assert set(ids) == {1, 2, 3, 4}
    assert [cid for cid, _ in fuse([1, 2], [2, 1], mode="vector")] == [1, 2]
    assert [cid for cid, _ in fuse([1, 2], [2, 1], mode="keyword")] == [2, 1]


def test_feedback_moves_ranking_but_is_bounded():
    assert feedback_multiplier(0, 0) == 1.0
    assert 1.0 < feedback_multiplier(1, 0) < feedback_multiplier(10, 0) <= 1 + FEEDBACK_WEIGHT
    assert 1 - FEEDBACK_WEIGHT <= feedback_multiplier(0, 50) < 1.0
    # Two chunks tied on rank: the up-voted one wins, the down-voted one loses.
    base = [cid for cid, _ in fuse([1, 2], [2, 1])]
    voted = [cid for cid, _ in fuse([1, 2], [2, 1], feedback={2: (3, 0)})]
    assert base == [1, 2] and voted == [2, 1]
    # Feedback alone cannot pull a chunk ranked 30th above the top hit.
    far = list(range(100, 130)) + [7]
    assert fuse(far, far, feedback={7: (100, 0)})[0][0] == 100


def hit(cid, title, content):
    return Hit(cid, f"doc-{cid}", title, content, 1.0)


def test_extractive_answer_cites_its_sources():
    hits = [hit(11, "Billing", chunk_text(DOC)[1].content), hit(12, "Billing", chunk_text(DOC)[0].content)]
    text = ExtractiveAnswerer().compose("Do you refund monthly plans?", hits)
    assert "Monthly plans are not refunded. [S1]" in text
    assert cited_chunks(text, hits) == [11]
    assert ExtractiveAnswerer().compose("kubernetes helm chart", hits) == NOT_FOUND
    assert ExtractiveAnswerer().compose("anything", []) == NOT_FOUND


def test_cited_chunks_ignores_out_of_range_and_duplicates():
    hits = [hit(1, "a", "x"), hit(2, "b", "y")]
    assert cited_chunks("A [S2] b [S2][S1] c [S9]", hits) == [2, 1]


def test_sources_escape_titles():
    out = format_sources([hit(1, 'Q&A "FAQ"', "body")])
    assert 'title="Q&amp;A &quot;FAQ&quot;"' in out and "body" in out
