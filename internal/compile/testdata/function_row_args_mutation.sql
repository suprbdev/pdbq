-- field: renameUser
SELECT coalesce(json_agg(__t_1::text), '[]'::json) AS data
FROM "public"."rename_user"("u" => jsonb_populate_record(NULL::"public"."users", $1::jsonb)::"public"."users", "new_name" => $2::"pg_catalog"."text") AS __t_1
-- args: ["{\"email\":\"grace@example.com\",\"id\":2}","Grace B. Hopper"]
-- then:
SELECT json_build_object('user', CASE WHEN __t_1 IS NULL THEN NULL ELSE json_build_object('id', __t_1."id", 'fullName', __t_1."full_name", 'postsByAuthorId', __l_6.data) END) AS data
FROM json_array_elements_text($1::json) AS __r_2(v)
CROSS JOIN LATERAL (SELECT (__r_2.v::"public"."users").*) AS __t_1
LEFT JOIN LATERAL (
  SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."posts" AS __c_5
  WHERE __c_5."author_id" = __t_1."id")::int) AS data
  FROM (
    SELECT row_number() OVER (ORDER BY __t_3."id" ASC) AS __rn
    FROM "public"."posts" AS __t_3
    WHERE __t_3."author_id" = __t_1."id"
    ORDER BY __t_3."id" ASC
    LIMIT 101
  ) AS __s_4
  GROUP BY ()
) AS __l_6 ON true
-- args: []

