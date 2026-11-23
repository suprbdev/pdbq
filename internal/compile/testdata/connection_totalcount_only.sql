-- field: allUsers
SELECT json_build_object('totalCount', (SELECT count(*) FROM "public"."users" AS __c_3)::int) AS data
FROM (
  SELECT row_number() OVER (ORDER BY __t_1."id" ASC) AS __rn
  FROM "public"."users" AS __t_1
  ORDER BY __t_1."id" ASC
  LIMIT 2
) AS __s_2
GROUP BY ()
-- args: null

