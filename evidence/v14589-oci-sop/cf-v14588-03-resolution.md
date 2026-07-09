CF-v14588-03 resolution audit
================================
Date: 2026-07-10 (v14589, Pair 7 Review)

Decision: DELETE /home/jaslian/.ssh/oracle_jump and /home/jaslian/.ssh/oracle_jump.pub.

Rationale:
1. The active operator key is /home/jaslian/.ssh/fleet_lan.pub (SHA-256 of file: 5785686b2340881a120e58bd85f346e6828e2c3bafface29dfb1660b336573f5).
2. We will push /home/jaslian/.ssh/fleet_lan.pub to oracle-jump:/home/ubuntu/.ssh/authorized_keys
   per CF-v14588-01, replacing the MacBook-era pubkey.
3. /home/jaslian/.ssh/oracle_jump.pub (SHA-256 of file: 02239ef0ef15a95f7ab0fa8bb9f6e4c79c6ec20d0fa43004e88a9929c98e0c4f) was generated locally on
   2026-07-09 but never pushed to any OCI instance.
4. Keeping it locally risks future confusion (operator might wonder which
   pubkey is on oracle-jump).

Archive location: 1Password item bsqycxxs2hxqyjiemxea7m47ae (MacBook-era
public_key field, fingerprint xQwBn5ANaEIxNNlz5BSBSYfy+FVf/MEn1OMAtmdLbcQ).

Action: rm -f /home/jaslian/.ssh/oracle_jump /home/jaslian/.ssh/oracle_jump.pub
