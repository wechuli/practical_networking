# Routing Evidence Exercise

Read [routing-observations.json](routing-observations.json) before visiting a live
looking glass. It is explicitly synthetic and date-stamped, so the exercise is
reproducible without pretending these are measurements from a real operator.

1. Which AS's selected route changed? Can this observation establish the affected
   customer's LOCAL_PREF or its selected route?
2. Does the RTT increase measure delay on one interconnect? What other paths or
   response-processing differences could contribute?
3. Does a longer AS-PATH prove a worse route under BGP policy?
4. Does a valid origin, if later established, validate every transit AS?
5. What would you send to the affected operator, and what private evidence would
   be required to confirm the configuration cause?

Reference answers: the observer's route changed; the customer's LOCAL_PREF is
unknown. RTT includes both directions and reply processing. Policy precedes path
length. Origin validation is not full path validation. Escalate timestamps,
source/destination prefixes, before/after collector views, application timing,
and healthy comparisons; request the affected operator's route/policy history.

For a live extension, record observation location, UTC time, queried prefix,
selected route, and whether the service exposes best-only or multiple paths.
Public websites at two hostnames are not proof of a controlled same-region test.
Do not label a conjecture about another operator's internal policy as confirmed.

Private interconnect is not inherently encryption or redundancy. Check supported
MACsec/IPsec options, diverse circuits/locations, return paths, and tested failover.
