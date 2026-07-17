-- field: allPosts
SELECT jsonb_build_object('nodes', coalesce(jsonb_agg(__s_2.ndata ORDER BY __s_2.__rn DESC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb), 'edges', coalesce(jsonb_agg(jsonb_build_object('cursor', __s_2.__cursor, 'node', __s_2.edata) ORDER BY __s_2.__rn DESC) FILTER (WHERE __s_2.__rn <= $4), '[]'::jsonb), 'pageInfo', jsonb_build_object('hasNextPage', true, 'hasPreviousPage', (count(*) > $4), 'startCursor', (jsonb_agg(__s_2.__cursor ORDER BY __s_2.__rn DESC) FILTER (WHERE __s_2.__rn <= $4))->0, 'endCursor', (jsonb_agg(__s_2.__cursor ORDER BY __s_2.__rn DESC) FILTER (WHERE __s_2.__rn <= $4))->-1)) AS data
FROM (
  SELECT jsonb_build_object('id', __t_1."id", 'title', __t_1."title") AS ndata, jsonb_build_object('id', __t_1."id") AS edata, replace(encode(convert_to(jsonb_build_array('Post'::text, __t_1."id")::text, 'UTF8'), 'base64'), E'\n', '') AS __cursor, row_number() OVER (ORDER BY __t_1."title" DESC, __t_1."id" DESC) AS __rn
  FROM "public"."posts" AS __t_1
  WHERE (__t_1."title" < (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) OR (__t_1."title" = (SELECT __a."title" FROM "public"."posts" AS __a WHERE __a."id" = $1) AND __t_1."id" < $2))
  ORDER BY __t_1."title" DESC, __t_1."id" DESC
  LIMIT $3
) AS __s_2
GROUP BY ()
-- args: ["9","9",3,2]

