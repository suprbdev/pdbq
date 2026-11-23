-- field: grabPosts
SELECT json_build_object('postRows', CASE WHEN __fn.v IS NULL THEN NULL ELSE (SELECT coalesce(json_agg(CASE WHEN ((__fn.v)[__e_1.o])::text IS NULL THEN NULL ELSE json_build_object('id', ((__fn.v)[__e_1.o])."id", 'title', ((__fn.v)[__e_1.o])."title") END ORDER BY __e_1.o), '[]'::json) FROM (SELECT generate_subscripts(__fn.v, 1) AS o) AS __e_1) END) AS data
FROM (SELECT "public"."grab_posts"() AS v) AS __fn
-- args: null

