-- field: bumpMood
SELECT jsonb_build_object('clientMutationId', NULL::text, 'result', to_jsonb(CASE __fn.v WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END)) AS data
FROM (SELECT "public"."bump_mood"($1::"mood") AS v) AS __fn
-- args: ["sad"]

