-- field: bumpMood
SELECT json_build_object('clientMutationId', NULL::text, 'result', to_json(CASE __fn.v WHEN 'happy' THEN 'HAPPY' WHEN 'ok' THEN 'OK' WHEN 'sad' THEN 'SAD' END)) AS data
FROM (SELECT "public"."bump_mood"("m" => $1::"public"."mood") AS v) AS __fn
-- args: ["sad"]

